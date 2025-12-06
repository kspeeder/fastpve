package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/linkease/fastpve/downloader"
	"github.com/linkease/fastpve/quickget"
	"github.com/linkease/fastpve/vmdownloader"
	"github.com/urfave/cli/v3"
)

func windowsCommand() *cli.Command {
	return &cli.Command{
		Name:  "windows",
		Usage: "Download Windows 7/10/11 ISO",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "version",
				Usage:   "Windows version: 7, 10 or 11",
				Value:   "11",
				Aliases: []string{"v"},
			},
			&cli.StringFlag{
				Name:  "edition",
				Usage: "Edition language, e.g. \"Chinese (Simplified)\"",
				Value: "Chinese (Simplified)",
			},
			&cli.BoolFlag{
				Name:  "resume",
				Usage: "Resume from existing status if present",
				Value: true,
			},
			&cli.BoolFlag{
				Name:  "virtio",
				Usage: "Also download VirtIO driver",
				Value: true,
			},
			&cli.StringFlag{
				Name:  "iso-path",
				Usage: "Directory for final ISO",
				Value: defaultISOPath,
			},
			&cli.StringFlag{
				Name:  "cache-path",
				Usage: "Directory for partial downloads/status files",
				Value: defaultCachePath,
			},
			&cli.StringFlag{
				Name:  "status-path",
				Usage: "Override status file path for Windows ISO",
			},
			&cli.StringFlag{
				Name:  "virtio-status-path",
				Usage: "Override status file path for VirtIO",
			},
		},
		Action: downloadWindows,
	}
}

func downloadWindows(ctx context.Context, cmd *cli.Command) error {
	isoPath := cmd.String("iso-path")
	cachePath := cmd.String("cache-path")
	if err := ensureDirs(isoPath, cachePath); err != nil {
		return err
	}

	statusPath := cmd.String("status-path")
	if statusPath == "" {
		statusPath = defaultStatusPath(cachePath, "windows_install.ops")
	}
	virtStatusPath := cmd.String("virtio-status-path")
	if virtStatusPath == "" {
		virtStatusPath = defaultStatusPath(cachePath, "windows_virtio.ops")
	}
	resume := cmd.Bool("resume")

	version, err := parseWindowsVersion(cmd.String("version"))
	if err != nil {
		return err
	}
	edition := strings.TrimSpace(cmd.String("edition"))
	if version == Win7 && edition == "" {
		edition = "Chinese (Simplified)"
	}
	if edition == "" {
		return errors.New("edition is required")
	}

	downer := downloader.NewDownloader()
	var status *downloader.DownloadStatus
	if resume {
		status, _ = vmdownloader.IsStatusValid(downer, statusPath)
	}

	quickGet, err := quickget.CreateQuickGet()
	if err != nil {
		return err
	}
	defer os.Remove(quickGet)

	target, err := vmdownloader.DownloadWindowsISO(ctx, downer, quickGet, isoPath, statusPath, status, version, edition)
	if err != nil {
		return err
	}
	fmt.Println("Windows ISO ready:", target)

	if cmd.Bool("virtio") {
		var virtStatus *downloader.DownloadStatus
		if resume {
			virtStatus, _ = vmdownloader.IsStatusValid(downer, virtStatusPath)
		}
		virtTarget, err := vmdownloader.DownloadVirtIO(ctx, downer, isoPath, virtStatusPath, virtStatus)
		if err != nil {
			return fmt.Errorf("virtio download failed: %w", err)
		}
		fmt.Println("VirtIO ISO ready:", virtTarget)
	}

	return nil
}
