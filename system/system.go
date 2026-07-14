package system

import (
	"context"
	"runtime"
	"time"

	"github.com/acobaugh/osrelease"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/parsers/kernel"
)

type Information struct {
	Version string            `json:"version"`
	Docker  DockerInformation `json:"docker"`
	System  System            `json:"system"`
	Time    TimeInformation   `json:"time"`
}

// TimeInformation reports the host's real, local system clock — as opposed to
// any per-server timezone configured on the Panel, which only affects the TZ
// env var inside a server's container, not the underlying node's own clock.
type TimeInformation struct {
	// RFC3339 timestamp of the node's current local time, e.g. "2026-07-14T02:15:30-06:00".
	Timestamp string `json:"timestamp"`
	// Local zone abbreviation as reported by the OS, e.g. "CST", "UTC".
	Timezone string `json:"timezone"`
	// Offset from UTC, in seconds, east of UTC (matches Go's time.Zone()).
	UTCOffsetSeconds int `json:"utc_offset_seconds"`
}

type DockerInformation struct {
	Version    string           `json:"version"`
	Cgroups    DockerCgroups    `json:"cgroups"`
	Containers DockerContainers `json:"containers"`
	Storage    DockerStorage    `json:"storage"`
	Runc       DockerRunc       `json:"runc"`
}

type DockerCgroups struct {
	Driver  string `json:"driver"`
	Version string `json:"version"`
}

type DockerContainers struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Paused  int `json:"paused"`
	Stopped int `json:"stopped"`
}

type DockerStorage struct {
	Driver     string `json:"driver"`
	Filesystem string `json:"filesystem"`
}

type DockerRunc struct {
	Version string `json:"version"`
}

type System struct {
	Architecture  string `json:"architecture"`
	CPUThreads    int    `json:"cpu_threads"`
	MemoryBytes   int64  `json:"memory_bytes"`
	KernelVersion string `json:"kernel_version"`
	OS            string `json:"os"`
	OSType        string `json:"os_type"`
}

func GetSystemInformation() (*Information, error) {
	k, err := kernel.GetKernelVersion()
	if err != nil {
		return nil, err
	}

	version, info, err := GetDockerInfo(context.Background())
	if err != nil {
		return nil, err
	}

	release, err := osrelease.Read()
	if err != nil {
		return nil, err
	}

	var os string
	if release["PRETTY_NAME"] != "" {
		os = release["PRETTY_NAME"]
	} else if release["NAME"] != "" {
		os = release["NAME"]
	} else {
		os = info.OperatingSystem
	}

	var filesystem string
	for _, v := range info.DriverStatus {
		if v[0] != "Backing Filesystem" {
			continue
		}
		filesystem = v[1]
		break
	}

	now := time.Now()
	zoneName, offsetSeconds := now.Zone()

	return &Information{
		Version: Version,
		Docker: DockerInformation{
			Version: version.Version,
			Cgroups: DockerCgroups{
				Driver:  info.CgroupDriver,
				Version: info.CgroupVersion,
			},
			Containers: DockerContainers{
				Total:   info.Containers,
				Running: info.ContainersRunning,
				Paused:  info.ContainersPaused,
				Stopped: info.ContainersStopped,
			},
			Storage: DockerStorage{
				Driver:     info.Driver,
				Filesystem: filesystem,
			},
			Runc: DockerRunc{
				Version: info.RuncCommit.ID,
			},
		},
		System: System{
			Architecture:  runtime.GOARCH,
			CPUThreads:    runtime.NumCPU(),
			MemoryBytes:   info.MemTotal,
			KernelVersion: k.String(),
			OS:            os,
			OSType:        runtime.GOOS,
		},
		Time: TimeInformation{
			Timestamp:         now.Format(time.RFC3339),
			Timezone:          zoneName,
			UTCOffsetSeconds:  offsetSeconds,
		},
	}, nil
}

func GetDockerInfo(ctx context.Context) (types.Version, system.Info, error) {
	// TODO: find a way to re-use the client from the docker environment.
	c, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return types.Version{}, system.Info{}, err
	}
	defer c.Close()

	dockerVersion, err := c.ServerVersion(ctx)
	if err != nil {
		return types.Version{}, system.Info{}, err
	}

	dockerInfo, err := c.Info(ctx)
	if err != nil {
		return types.Version{}, system.Info{}, err
	}

	return dockerVersion, dockerInfo, nil
}
