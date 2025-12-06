package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/linkease/fastpve/downloader"
	"github.com/linkease/fastpve/quickget"
	"github.com/linkease/fastpve/utils"
	"github.com/linkease/fastpve/vmdownloader"
	"github.com/manifoldco/promptui"
)

var winEditions = []string{
	"Chinese (Simplified)",
	"Chinese (Traditional)",
	"English (United States)",
	"English International",
	"English Enterprise",
}

var win7Editions = []string{
	"Chinese (Simplified) x64",
	"English Enterprise",
}

const (
	Win11 = iota
	Win10
	Win7
)

type windowsInstallInfo struct {
	WindowISO    string `json:"windowISO"`
	VirtIO       string `json:"virtio"`
	WinVersion   int    `json:"winVersion"` // 0:11, 1:10, 2:7
	WinEdition   int    `json:"winEdition"`
	Memory       int    `json:"memory"`
	Cores        int    `json:"cores"`
	Disk         int    `json:"disk"`
	DownloadOnly bool   `json:"downloadOnly"`
}

func promptInstallWindows() error {
	isoPath := "/var/lib/vz/template/iso/"
	cachePath := "/var/lib/vz/template/cache"
	downer := newDownloader()
	statusPath := filepath.Join(cachePath, "windows_install.ops")
	status, _ := vmdownloader.IsStatusValid(downer, statusPath)
	registerGHCRMirrorPrompt()

	var windows []string
	var virtio []string
	dirs, err := os.ReadDir(isoPath)
	if err == nil {
		windows = getWindowISO(dirs)
		virtio = getVirtIOISO(dirs)
	}

	info := &windowsInstallInfo{
		WinVersion: -1,
		WinEdition: -1,
	}

	err = promptWinFiles(info, status, windows)
	if err != nil {
		return err
	}
	if len(virtio) > 0 {
		prompt := promptui.Select{
			Label: "选择VirtIO驱动文件",
			Items: virtio,
		}
		var file string
		_, file, err = prompt.Run()
		if err != nil {
			return err
		}
		info.VirtIO = file
	}
	info.Cores, err = promptPVECore()
	if err != nil {
		return err
	}
	info.Memory, err = promptPVEMemory()
	if err != nil {
		return err
	}
	info.Disk, err = promptPVEDisk()
	if err != nil {
		return err
	}

	fmt.Println("install=", utils.ToString(info))
	var needDownload bool
	if (status != nil && info.WindowISO == status.TargetFile) ||
		info.WinVersion >= 0 && info.WinEdition >= 0 ||
		info.VirtIO == "" {
		needDownload = true
	}
	next, err := promptWinDownloadInstall(info, needDownload)
	if err != nil {
		return err
	}
	if !next {
		return nil
	}

	ctx := context.TODO()
	var hasJq, uuidgen bool
	if _, err := exec.LookPath("jq"); err == nil {
		hasJq = true
	}
	if _, err := exec.LookPath("uuidgen"); err == nil {
		uuidgen = true
	}
	if !hasJq || !uuidgen {
		fmt.Println("安装缺失的 jq uuidgen")
		utils.BatchRunStdout(ctx, []string{"apt update && apt install -y jq uuid-runtime"}, 0)
		if _, err := exec.LookPath("jq"); err == nil {
			hasJq = true
		}
		if _, err := exec.LookPath("uuidgen"); err == nil {
			uuidgen = true
		}
		if !hasJq {
			fmt.Println("缺少 jq 而且再次安装也失败")
			return errors.New("缺少 jq")
		}
		if !uuidgen {
			fmt.Println("缺少 uuidgen 而且再次安装也失败")
			return errors.New("缺少 uuidgen")
		}
	}

	quickGet, err := quickget.CreateQuickGet()
	if err != nil {
		return err
	}
	defer os.Remove(quickGet)
	//log.Println("quickGet=", quickGet)

	if status != nil && info.WindowISO == status.TargetFile {
		// Continue download target file
		info.WindowISO, err = vmdownloader.DownloadWindowsISO(ctx, downer, quickGet, isoPath, statusPath, status, -1, "")
		if err != nil {
			return err
		}
	}
	if info.WinVersion >= 0 && info.WinEdition >= 0 {
		editionName, err := selectedEdition(info)
		if err != nil {
			return err
		}
		info.WindowISO, err = vmdownloader.DownloadWindowsISO(ctx, downer, quickGet, isoPath, statusPath, status, info.WinVersion, editionName)
		if err != nil {
			return err
		}
	}

	if info.VirtIO == "" {
		virtStatusPath := filepath.Join(cachePath, "windows_virtio.ops")
		virtStatus, _ := vmdownloader.IsStatusValid(downer, virtStatusPath)
		info.VirtIO, err = vmdownloader.DownloadVirtIO(ctx, downer, isoPath, virtStatusPath, virtStatus)
		if err != nil {
			return err
		}
	}
	if info.DownloadOnly {
		return nil
	}

	return createWindowVM(ctx, info)
}

func getWindowISO(dirs []os.DirEntry) []string {
	var isoFiles []string
	for _, dir := range dirs {
		if !dir.IsDir() &&
			strings.HasPrefix(dir.Name(), "windows-") &&
			filepath.Ext(dir.Name()) == ".iso" {
			isoFiles = append(isoFiles, dir.Name())
		}
	}
	return isoFiles
}

func getVirtIOISO(dirs []os.DirEntry) []string {
	var isoFiles []string
	for _, dir := range dirs {
		if !dir.IsDir() &&
			strings.HasPrefix(dir.Name(), "virtio-win-") &&
			filepath.Ext(dir.Name()) == ".iso" {
			isoFiles = append(isoFiles, dir.Name())
		}
	}
	return isoFiles
}

func promptWinFiles(info *windowsInstallInfo,
	status *downloader.DownloadStatus,
	windows []string) error {
	origWinLen := len(windows)
	if status != nil {
		name := filepath.Base(status.TargetFile)
		name = strings.TrimSuffix(name, ".syn")
		progress := status.Curr * 100 / (status.TotalSize + 1)
		name = fmt.Sprintf("继续下载 %s(%02d%%)", name, progress)
		windows = append(windows, name)
	}
	newOptions := []struct {
		Label   string
		Version int
	}{
		{"全新下载 Windows11", Win11},
		{"全新下载 Windows10", Win10},
		{"全新下载 Windows7 (支持简体/英文)", Win7},
	}
	startNew := len(windows)
	for _, opt := range newOptions {
		windows = append(windows, opt.Label)
	}
	prompt := promptui.Select{
		Label: "选择Windows安装文件",
		Items: windows,
	}
	idx, file, err := prompt.Run()
	if err != nil {
		return err
	}
	var selWin bool
	if idx < origWinLen {
		info.WindowISO = file
	} else if status != nil && idx == origWinLen {
		info.WindowISO = status.TargetFile
	} else if idx >= startNew {
		selWin = true
		opt := newOptions[idx-startNew]
		info.WinVersion = opt.Version
		if opt.Version == Win7 {
			err = promptWin7Edition(info)
		} else {
			err = promptWinEdition(info)
			if err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	} else {
		// Should not reach.
	}

	if !selWin {
		prompt := promptui.Select{
			Label: "选择系统：",
			Items: []string{"11", "10", "7"},
		}
		idx, _, err := prompt.Run()
		if err != nil {
			return err
		}
		info.WinVersion = idx
		if info.WinVersion == Win7 && info.WinEdition < 0 {
			info.WinEdition = findEditionIndex(win7Editions, "Chinese (Simplified) x64")
		}
	}

	return nil
}

func findEditionIndex(options []string, name string) int {
	for i, v := range options {
		if v == name {
			return i
		}
	}
	return -1
}

func selectedEdition(info *windowsInstallInfo) (string, error) {
	if info.WinEdition < 0 {
		return "", errors.New("未选择 Windows 版本语言")
	}
	if info.WinVersion == Win7 {
		if info.WinEdition >= len(win7Editions) {
			return "", fmt.Errorf("无效的 Windows 7 版本选项: %d", info.WinEdition)
		}
		return win7Editions[info.WinEdition], nil
	}
	if info.WinEdition >= len(winEditions) {
		return "", fmt.Errorf("无效的 Windows 版本选项: %d", info.WinEdition)
	}
	return winEditions[info.WinEdition], nil
}

func promptWinEdition(info *windowsInstallInfo) error {
	idx, err := promptEdition("Windows版本语言", winEditions)
	if err != nil {
		return err
	}
	info.WinEdition = idx
	return nil
}

func promptWin7Edition(info *windowsInstallInfo) error {
	idx, err := promptEdition("Windows 7 语言/架构", win7Editions)
	if err != nil {
		return err
	}
	info.WinEdition = idx
	return nil
}

func promptEdition(label string, editions []string) (int, error) {
	prompt := promptui.Select{
		Label: label,
		Items: editions,
	}
	idx, _, err := prompt.Run()
	return idx, err
}

func promptWinDownloadInstall(info *windowsInstallInfo, needDownload bool) (bool, error) {
	var items []string
	if needDownload {
		items = []string{"下载并安装", "仅下载", "退出"}
	} else {
		items = []string{"安装", "退出"}
	}
	prompt := promptui.Select{
		Label: fmt.Sprintf("选择完成，继续安装%s：（CPU：%d,内存：%dMB,硬盘：%dGB）",
			filepath.Base(info.WindowISO),
			info.Cores,
			info.Memory,
			info.Disk),
		Items: items,
	}
	idx, _, err := prompt.Run()
	if err != nil {
		return false, err
	}
	if idx == 0 {
		return true, nil
	}
	if needDownload {
		if idx == 1 {
			info.DownloadOnly = true
			return true, nil
		}
	}
	return false, nil
}

var (
	ghcrMirrorOnce sync.Once
	ghcrMirrors    []string
	ghcrMirrorErr  error
)

func registerGHCRMirrorPrompt() {
	vmdownloader.GHCRMirrorSelector = func(ctx context.Context, reference string) ([]string, error) {
		ghcrMirrorOnce.Do(func() {
			ghcrMirrors, ghcrMirrorErr = promptGHCRMirrors()
		})
		if ghcrMirrorErr != nil {
			return nil, ghcrMirrorErr
		}
		return ghcrMirrors, nil
	}
}

func promptGHCRMirrors() ([]string, error) {
	options := []string{
		"ghcr.1ms.run（默认，自动回退官方）",
		"ghcr.nju.edu.cn（自动回退官方）",
		"ghcr.linkease.net:5443（需安装 KSpeeder，最快）",
		"ghcr.io（仅使用官方）",
	}
	prompt := promptui.Select{
		Label: "选择 GHCR 镜像源（用于 GHCR 备份下载）",
		Items: options,
	}
	idx, _, err := prompt.Run()
	if err != nil {
		return nil, err
	}
	switch idx {
	case 0:
		return []string{"ghcr.1ms.run"}, nil
	case 1:
		return []string{"ghcr.nju.edu.cn"}, nil
	case 2:
		return []string{"ghcr.linkease.net:5443"}, nil
	case 3:
		return []string{}, nil
	default:
		return nil, nil
	}
}

func createWindowVM(ctx context.Context, info *windowsInstallInfo) error {
	disks, err := quickget.DiskStatus()
	if err != nil {
		return err
	}
	useDisk := "local"
	if len(disks) > 0 {
		useDisk = disks[0]
	}
	for _, disk := range disks {
		if disk == "local-lvm" {
			useDisk = "local-lvm"
			break
		}
	}

	items, err := quickget.QMList()
	if err != nil {
		return err
	}
	vmid := 100
	if len(items) > 0 {
		sort.Slice(items, func(i, j int) bool {
			return items[i].VMID < items[j].VMID
		})
		vmid = items[len(items)-1].VMID + 1
	}
	winID := 10
	tpmStr := `echo win10`
	switch info.WinVersion {
	case Win11:
		winID = 11
		tpmStr = fmt.Sprintf(`qm set $VMID -tpmstate0 %s:1,version=v2.0`, useDisk)
	case Win7:
		winID = 7
		tpmStr = `echo win7`
	}
	winName := filepath.Base(info.WindowISO)
	vmName := toBetterWindowName(winName)
	bios := "ovmf"
	machine := "q35"
	needEFI := true
	if info.WinVersion == Win7 {
		bios = "seabios"
		needEFI = false
	}
	scripts := []string{
		"set -e",
		`export LC_ALL="en_US.UTF-8"`,
		fmt.Sprintf("export VMID=%d", vmid),
		fmt.Sprintf(`qm create $VMID --name "%s" --memory %d --scsihw virtio-scsi-single --cores %d --sockets 1 --machine %s --bios %s --cpu host --net0 virtio,bridge=vmbr0`,
			vmName,
			info.Memory,
			info.Cores,
			machine,
			bios),
	}
	if needEFI {
		scripts = append(scripts, fmt.Sprintf("qm set $VMID -efidisk0 %s:1,format=raw,efitype=4m,pre-enrolled-keys=1", useDisk))
	}
	scripts = append(scripts,
		fmt.Sprintf("qm set $VMID --scsi0 %s:64", useDisk),
		fmt.Sprintf(`qm set $VMID --ide0 local:iso/%s,media=cdrom`, winName),
		fmt.Sprintf(`qm set $VMID --ide1 local:iso/%s,media=cdrom`, filepath.Base(info.VirtIO)),
		`qm set $VMID --boot order='scsi0;ide0;ide1'`,
		`qm set $VMID --agent enabled=1,fstrim_cloned_disks=1`,
		tpmStr,
		fmt.Sprintf("qm set %d --ostype win%d", vmid, winID),
		`echo "VMOK"`,
	)
	//fmt.Println(strings.Join(scripts, "\n"))
	out, err := utils.BatchOutput(ctx, scripts, 0)
	if err != nil {
		return err
	}
	if strings.Contains(string(out), "VMOK") {
		fmt.Println("创建虚拟机：", vmid, "成功，请到网页端启动虚拟机并继续安装系统")
		return nil
	}
	return errors.New("VM creation failed")
}
