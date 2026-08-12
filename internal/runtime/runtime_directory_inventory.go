package runtime

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

var errLeaseManifestSizeInvalid = errors.New("lease manifest size is invalid")

type runtimeDirectoryScanner struct {
	readDir      func(string) ([]os.DirEntry, error)
	lstat        func(string) (os.FileInfo, error)
	readManifest func(string, os.FileInfo) ([]byte, error)
}

// InventoryRuntimeDirectories 只枚举受管 run root 的直接子目录及其固定 lease.json。
// 它不会递归用户文件、创建目录、修复权限或删除任何对象。
func InventoryRuntimeDirectories(runRoot string) ([]RuntimeDirectoryObservation, error) {
	if !filepath.IsAbs(runRoot) {
		return nil, errors.New("runtime directory inventory root must be absolute")
	}
	scanner := runtimeDirectoryScanner{
		readDir: os.ReadDir, lstat: os.Lstat, readManifest: readLeaseManifestNoFollow,
	}
	return scanner.inventory(filepath.Clean(runRoot))
}

func (s runtimeDirectoryScanner) inventory(runRoot string) ([]RuntimeDirectoryObservation, error) {
	rootInfo, err := s.lstat(runRoot)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("runtime directory inventory root is unavailable")
	}
	entries, err := s.readDir(runRoot)
	if err != nil {
		return nil, errors.New("runtime directory inventory root is unreadable")
	}
	result := make([]RuntimeDirectoryObservation, 0, len(entries))
	for _, entry := range entries {
		observation := RuntimeDirectoryObservation{}
		if !validLeaseSandboxID(entry.Name()) {
			observation.DiscoveryIssue = DiscoveryDirectoryNameInvalid
			result = append(result, observation)
			continue
		}
		observation.SandboxID = entry.Name()
		directory := filepath.Join(runRoot, entry.Name())
		info, err := s.lstat(directory)
		if err != nil {
			observation.DiscoveryIssue = DiscoveryDirectoryInspectUnavailable
			result = append(result, observation)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			observation.DiscoveryIssue = DiscoveryDirectoryUnsafe
			result = append(result, observation)
			continue
		}
		observation.DirectoryPresent = true
		s.inspectManifest(directory, &observation)
		result = append(result, observation)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].SandboxID < result[right].SandboxID
	})
	return result, nil
}

func (s runtimeDirectoryScanner) inspectManifest(directory string, observation *RuntimeDirectoryObservation) {
	target := filepath.Join(directory, LeaseManifestName)
	info, err := s.lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	observation.ManifestPresent = true
	if err != nil {
		observation.DiscoveryIssue = DiscoveryManifestUnavailable
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !leaseManifestModeSafe(info) || !leaseManifestOwnerSafe(info) {
		observation.DiscoveryIssue = DiscoveryManifestUnsafe
		return
	}
	content, err := s.readManifest(target, info)
	if err != nil {
		if errors.Is(err, errLeaseManifestSizeInvalid) {
			observation.DiscoveryIssue = DiscoveryManifestInvalid
		} else {
			observation.DiscoveryIssue = DiscoveryManifestUnavailable
		}
		return
	}
	manifest, err := DecodeLeaseManifest(content)
	if err != nil || manifest.SandboxID != observation.SandboxID {
		observation.DiscoveryIssue = DiscoveryManifestInvalid
		return
	}
	copy := manifest
	observation.Manifest = &copy
}

func readLeaseManifestContent(file *os.File) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, MaxLeaseManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxLeaseManifestBytes {
		return content, errLeaseManifestSizeInvalid
	}
	return content, nil
}
