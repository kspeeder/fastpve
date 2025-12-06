package vmdownloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kspeeder/blobDownload/blobDownloader"
	"github.com/kspeeder/docker-registry/lib"
	"github.com/linkease/fastpve/utils"
)

const (
	defaultGHCRMirror = "ghcr.1ms.run"
)

var (
	defaultGHCRMirrors = []string{defaultGHCRMirror}
	// GHCRMirrorSelector, when set, is used to obtain user preferred registry mirrors before attempting a download.
	GHCRMirrorSelector func(ctx context.Context, reference string) ([]string, error)
)

func downloadWindowsFromGHCR(ctx context.Context, isoPath string, version int, edition string) (string, error) {
	ref, err := ghcrWindowsReference(version, edition)
	if err != nil {
		return "", err
	}

	mirrors := defaultGHCRMirrors
	if GHCRMirrorSelector != nil {
		selected, err := GHCRMirrorSelector(ctx, ref)
		if err != nil {
			return "", err
		}
		if selected != nil {
			mirrors = selected
		}
	}
	refs := buildGHCRReferences(ref, mirrors)

	var lastErr error
	for _, candidate := range refs {
		target, err := fetchGHCRArtifact(ctx, candidate, isoPath)
		if err == nil {
			return target, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no GHCR reference candidates")
	}
	return "", fmt.Errorf("GHCR fallback failed: %w", lastErr)
}

func fetchGHCRArtifact(ctx context.Context, reference, isoPath string) (string, error) {
	api, refspec, err := buildRegistryClient(reference)
	if err != nil {
		return "", err
	}
	dl, err := blobDownloader.New(ctx, api, refspec)
	if err != nil {
		return "", err
	}
	files := dl.Files()
	if len(files) == 0 {
		return "", errors.New("GHCR package contains no files")
	}
	entry := files[0]
	dest := filepath.Join(isoPath, entry.Name)
	if info, err := os.Stat(dest); err == nil && info.Size() == entry.Size {
		return dest, nil
	}

	temp := dest + ".syn"
	start := int64(0)
	if info, err := os.Stat(temp); err == nil {
		start = info.Size()
		if start > entry.Size {
			start = 0
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := out.Seek(start, io.SeekStart); err != nil {
		return "", err
	}

	reader, err := dl.ReaderAt(ctx, entry.Name, start)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	var written int64
	stopCh := make(chan struct{})
	go reportGHCRProgress(entry.Name, entry.Size, start, &written, stopCh)

	if _, err := io.Copy(io.MultiWriter(out, &progressWriter{counter: &written}), reader); err != nil {
		close(stopCh)
		return "", err
	}
	close(stopCh)
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}

	if err := os.Rename(temp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func buildRegistryClient(reference string) (lib.RegistryApi, lib.Refspec, error) {
	host, repo, tag, err := parseRegistryReference(reference)
	if err != nil {
		return nil, nil, err
	}

	user, pass := ghcrCredentials()
	cfg := lib.NewConfig()
	cfg.SetUrl(url.URL{Scheme: "https", Host: host})
	cfg.SetAllowInsecure(false)
	cfg.SetCredentials(lib.NewRegistryCredentials(user, pass))

	api, err := lib.NewRegistryApi(cfg)
	if err != nil {
		return nil, nil, err
	}
	return api, lib.NewRefspec(repo, tag), nil
}

func ghcrCredentials() (string, string) {
	user := envDefault("GHCR_USERNAME", envDefault("GITHUB_ACTOR", ""))
	pass := envDefault("GHCR_PASSWORD", envDefault("GITHUB_TOKEN", ""))
	return user, pass
}

func envDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func swapRegistryHost(reference, host string) string {
	if host == "" {
		return reference
	}
	host = strings.TrimSpace(host)
	refHost, repo, tag, err := parseRegistryReference(reference)
	if err != nil {
		return reference
	}
	if refHost == host {
		return reference
	}
	return fmt.Sprintf("%s/%s:%s", host, repo, tag)
}

func buildGHCRReferences(ref string, mirrors []string) []string {
	refs := make([]string, 0, len(mirrors)+1)
	seen := make(map[string]struct{}, len(mirrors)+1)
	for _, mirror := range mirrors {
		mirror = strings.TrimSpace(mirror)
		if mirror == "" {
			continue
		}
		candidate := swapRegistryHost(ref, mirror)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		refs = append(refs, candidate)
	}
	if _, ok := seen[ref]; !ok {
		refs = append(refs, ref)
	}
	return refs
}

func ghcrWindowsReference(version int, edition string) (string, error) {
	switch version {
	case Win11:
		if !strings.EqualFold(edition, "Chinese (Simplified)") {
			return "", fmt.Errorf("GHCR Windows 11 仅支持 Chinese (Simplified)")
		}
		return "ghcr.io/kspeeder/win11x64:cn_simplified", nil
	case Win10:
		if !strings.EqualFold(edition, "Chinese (Simplified)") {
			return "", fmt.Errorf("GHCR Windows 10 仅支持 Chinese (Simplified)")
		}
		return "ghcr.io/kspeeder/win10x64:cn_simplified", nil
	case Win7:
		edition = strings.TrimSpace(edition)
		switch {
		case edition == "" || strings.EqualFold(edition, "English Enterprise"):
			return "ghcr.io/kspeeder/win7x64:en_enterprise", nil
		case strings.EqualFold(edition, "Chinese (Simplified)") || strings.EqualFold(edition, "Chinese (Simplified) x64"):
			return "ghcr.io/kspeeder/win7x64:cn_simplified", nil
		default:
			return "", fmt.Errorf("GHCR Windows 7 仅支持 English Enterprise 或 Chinese (Simplified)")
		}
	default:
		return "", fmt.Errorf("unsupported Windows version for GHCR: %d", version)
	}
}

func parseRegistryReference(reference string) (host, repo, tag string, err error) {
	ref := strings.TrimSpace(reference)
	if ref == "" {
		return "", "", "", errors.New("empty reference")
	}
	slash := strings.IndexByte(ref, '/')
	if slash <= 0 {
		return "", "", "", fmt.Errorf("reference %q missing registry host", reference)
	}
	host = ref[:slash]
	remainder := ref[slash+1:]
	colon := strings.LastIndexByte(remainder, ':')
	if colon <= 0 || colon == len(remainder)-1 {
		return "", "", "", fmt.Errorf("reference %q must include a tag", reference)
	}
	repo = remainder[:colon]
	tag = remainder[colon+1:]
	return host, repo, tag, nil
}

type progressWriter struct {
	counter *int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	if p.counter != nil {
		atomic.AddInt64(p.counter, int64(n))
	}
	return n, nil
}

func reportGHCRProgress(name string, total, start int64, written *int64, stopCh <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	lastBytes := start
	lastTime := time.Now()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			curr := atomic.LoadInt64(written) + start
			delta := curr - lastBytes
			elapsed := time.Since(lastTime).Seconds()
			speed := float64(delta) / (elapsed + 1e-6)
			percent := curr * 100 / (total + 1)
			fmt.Printf("GHCR downloading %s: %02d%%, %s/s\n", name, percent, utils.ByteCountDecimal(uint64(speed)))
			lastBytes = curr
			lastTime = time.Now()
		}
	}
}
