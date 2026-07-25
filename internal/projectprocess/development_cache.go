package projectprocess

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const developmentBuildCacheDirectoryName = "development-build-cache"

// developmentBuildCache retains rooted removal authority for Harbor's fallback shared Go cache.
type developmentBuildCache struct {
	parent *os.Root
	name   string
	path   string
}

// developmentBuildCacheLease keeps one accepted process counted until its complete scope settles.
type developmentBuildCacheLease struct {
	supervisor *Supervisor
	path       string
	owned      bool
	released   bool
}

// developmentSharedGoCache returns only a clean absolute captured cache suitable for isolated artifact roots.
func developmentSharedGoCache(environment []string) string {
	value := ""
	for _, entry := range environment {
		name, candidate, ok := strings.Cut(entry, "=")
		if ok && environmentNameEqual(name, "GOCACHE") {
			value = strings.TrimSpace(candidate)
		}
	}
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return ""
	}
	return value
}

// prepareDevelopmentBuildCache opens one owner-private shared cache beneath Harbor's runtime directory.
func prepareDevelopmentBuildCache(directory string) (*developmentBuildCache, error) {
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, errors.New("development build cache directory must be a clean absolute path")
	}
	information, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect development build cache parent: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return nil, errors.New("development build cache parent is not a direct directory")
	}
	parent, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open development build cache parent: %w", err)
	}
	if err := parent.MkdirAll(developmentBuildCacheDirectoryName, 0o700); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("create development build cache: %w", err)
	}
	cacheInformation, err := parent.Lstat(developmentBuildCacheDirectoryName)
	if err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("inspect development build cache: %w", err)
	}
	if cacheInformation.Mode()&os.ModeSymlink != 0 || !cacheInformation.IsDir() {
		_ = parent.Close()
		return nil, errors.New("development build cache is not an owner-private directory")
	}
	if err := parent.Chmod(developmentBuildCacheDirectoryName, 0o700); err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("secure development build cache: %w", err)
	}
	return &developmentBuildCache{
		parent: parent,
		name:   developmentBuildCacheDirectoryName,
		path:   filepath.Join(directory, developmentBuildCacheDirectoryName),
	}, nil
}

// remove deletes the shared cache only after every lease proves its process scope settled.
func (cache *developmentBuildCache) remove() error {
	if cache == nil || cache.parent == nil {
		return nil
	}
	removeErr := cache.parent.RemoveAll(cache.name)
	closeErr := cache.parent.Close()
	cache.parent = nil
	if removeErr != nil {
		removeErr = fmt.Errorf("remove development build cache: %w", removeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close development build cache parent: %w", closeErr)
	}
	return errors.Join(removeErr, closeErr)
}

// acquireDevelopmentBuildCache selects the caller's cache or leases Harbor's shared fallback.
func (supervisor *Supervisor) acquireDevelopmentBuildCache() (*developmentBuildCacheLease, error) {
	if cache := developmentSharedGoCache(supervisor.environment); cache != "" {
		return &developmentBuildCacheLease{path: cache}, nil
	}
	supervisor.developmentCacheMu.Lock()
	defer supervisor.developmentCacheMu.Unlock()
	if supervisor.developmentCache == nil {
		cache, err := prepareDevelopmentBuildCache(supervisor.developmentArtifactDirectory)
		if err != nil {
			return nil, err
		}
		supervisor.developmentCache = cache
	}
	supervisor.developmentCacheUsers++
	return &developmentBuildCacheLease{
		supervisor: supervisor,
		path:       supervisor.developmentCache.path,
		owned:      true,
	}, nil
}

// release retires one cache lease and removes the fallback only after every user settled.
func (lease *developmentBuildCacheLease) release(settled bool) error {
	if lease == nil || lease.released {
		return nil
	}
	lease.released = true
	if !lease.owned {
		return nil
	}
	supervisor := lease.supervisor
	supervisor.developmentCacheMu.Lock()
	defer supervisor.developmentCacheMu.Unlock()
	if supervisor.developmentCacheUsers <= 0 {
		return errors.New("development build cache lease count is invalid")
	}
	supervisor.developmentCacheUsers--
	if !settled {
		supervisor.developmentCacheUncertain = true
	}
	if supervisor.developmentCacheUsers != 0 || supervisor.developmentCacheUncertain {
		return nil
	}
	err := supervisor.developmentCache.remove()
	supervisor.developmentCache = nil
	return err
}
