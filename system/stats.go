package system

import (
	"runtime"
	"strings"
	"sync"
	"time"

	gopsutilcpu "github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

// statsSampleInterval is how often the background sampler refreshes the
// cached Stats snapshot. GET /api/system/stats always reads the cache so
// the request never blocks on its own sampling window.
const statsSampleInterval = time.Second

type Stats struct {
	CPU     CPUStats     `json:"cpu"`
	Memory  MemoryStats  `json:"memory"`
	Disk    DiskStats    `json:"disk"`
	Swap    SwapStats    `json:"swap"`
	DiskIO  DiskIOStats  `json:"disk_io"`
	Network NetworkStats `json:"network"`
}

type CPUStats struct {
	UsagePercent float64 `json:"usage_percent"`
	Threads      int     `json:"threads"`
	ModelName    string  `json:"model_name"`
}

type MemoryStats struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type DiskStats struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type SwapStats struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type DiskIOStats struct {
	ReadBytesPerSecond  uint64 `json:"read_bytes_per_second"`
	WriteBytesPerSecond uint64 `json:"write_bytes_per_second"`
}

type NetworkStats struct {
	RxBytesPerSecond uint64 `json:"rx_bytes_per_second"`
	TxBytesPerSecond uint64 `json:"tx_bytes_per_second"`
}

var statsCache = struct {
	sync.RWMutex
	value Stats
}{}

var statsSamplerOnce sync.Once

// StartStatsSampler launches the background goroutine that keeps the stats
// cache warm. It is safe to call more than once; only the first call has any
// effect. diskPath should be the filesystem holding server data (typically
// config.Get().System.RootDirectory) so "disk usage" reflects the volume
// that actually fills up as servers are used, not just the OS partition.
func StartStatsSampler(diskPath string) {
	statsSamplerOnce.Do(func() {
		// disk.IOCounters() reports every block device AND every one of its
		// partitions separately (e.g. "vda" and "vda1" both include the same
		// underlying bytes) — summing all of them double/triple counts I/O.
		// Resolve the single device that actually backs diskPath up front and
		// only ever read counters for that one device.
		diskDevice := resolveDiskDevice(diskPath)

		// The processor model never changes for the life of the process, so
		// resolve it once here instead of paying cpu.Info()'s ~120µs cost on
		// every single tick forever.
		modelName := cpuModelName()

		// Prime gopsutil's internal delta tracking for both CPU and IO
		// counters before the first real sample, otherwise the first
		// couple of reported values would be meaningless cumulative
		// totals rather than rates.
		_, _ = gopsutilcpu.Percent(0, false)
		prevDiskIO, _ := disk.IOCounters()
		prevNetIO, _ := net.IOCounters(false)
		prevSampleTime := time.Now()

		sample := func() {
			now := time.Now()
			elapsed := now.Sub(prevSampleTime).Seconds()
			if elapsed <= 0 {
				elapsed = statsSampleInterval.Seconds()
			}

			s := Stats{
				CPU: CPUStats{
					Threads:   runtime.NumCPU(),
					ModelName: modelName,
				},
			}

			if percents, err := gopsutilcpu.Percent(0, false); err == nil && len(percents) > 0 {
				s.CPU.UsagePercent = percents[0]
			}

			if vm, err := mem.VirtualMemory(); err == nil {
				s.Memory = MemoryStats{
					UsedBytes:  vm.Used,
					TotalBytes: vm.Total,
					Percent:    vm.UsedPercent,
				}
			}

			if sm, err := mem.SwapMemory(); err == nil {
				s.Swap = SwapStats{
					UsedBytes:  sm.Used,
					TotalBytes: sm.Total,
					Percent:    sm.UsedPercent,
				}
			}

			if du, err := disk.Usage(diskPath); err == nil {
				s.Disk = DiskStats{
					UsedBytes:  du.Used,
					TotalBytes: du.Total,
					Percent:    du.UsedPercent,
				}
			}

			if counters, err := disk.IOCounters(); err == nil && diskDevice != "" {
				var readDelta, writeDelta uint64
				if c, ok := counters[diskDevice]; ok {
					if prev, ok := prevDiskIO[diskDevice]; ok {
						readDelta = c.ReadBytes - prev.ReadBytes
						writeDelta = c.WriteBytes - prev.WriteBytes
					}
				}
				prevDiskIO = counters
				s.DiskIO = DiskIOStats{
					ReadBytesPerSecond:  uint64(float64(readDelta) / elapsed),
					WriteBytesPerSecond: uint64(float64(writeDelta) / elapsed),
				}
			}

			if counters, err := net.IOCounters(false); err == nil && len(counters) > 0 {
				var rxDelta, txDelta uint64
				if len(prevNetIO) > 0 {
					rxDelta = counters[0].BytesRecv - prevNetIO[0].BytesRecv
					txDelta = counters[0].BytesSent - prevNetIO[0].BytesSent
				}
				prevNetIO = counters
				s.Network = NetworkStats{
					RxBytesPerSecond: uint64(float64(rxDelta) / elapsed),
					TxBytesPerSecond: uint64(float64(txDelta) / elapsed),
				}
			}

			prevSampleTime = now

			statsCache.Lock()
			statsCache.value = s
			statsCache.Unlock()
		}

		// Take one sample immediately so the first API call after boot
		// doesn't have to wait a full tick for data to exist.
		sample()

		go func() {
			ticker := time.NewTicker(statsSampleInterval)
			defer ticker.Stop()
			for range ticker.C {
				sample()
			}
		}()
	})
}

// GetStats returns the most recently sampled host resource stats. Call
// StartStatsSampler once during daemon boot before relying on this; if the
// sampler has never run this simply returns a zero-value Stats.
func GetStats() Stats {
	statsCache.RLock()
	defer statsCache.RUnlock()
	return statsCache.value
}

// resolveDiskDevice finds the block device backing the filesystem that
// contains path, by matching the longest mountpoint prefix among all mounted
// partitions (the same approach `df` uses). Returns a bare device name (e.g.
// "vda1") matching the keys returned by disk.IOCounters(), or "" if it can't
// be determined.
func resolveDiskDevice(path string) string {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return ""
	}

	device, bestLen := "", -1
	for _, p := range partitions {
		if strings.HasPrefix(path, p.Mountpoint) && len(p.Mountpoint) > bestLen {
			device, bestLen = p.Device, len(p.Mountpoint)
		}
	}

	return strings.TrimPrefix(device, "/dev/")
}

func cpuModelName() string {
	info, err := gopsutilcpu.Info()
	if err != nil || len(info) == 0 {
		return ""
	}
	return info[0].ModelName
}
