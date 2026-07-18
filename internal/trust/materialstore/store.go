package materialstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goforj/harbor/internal/platform/userpaths"
	"github.com/goforj/harbor/internal/trust/localca"
)

const (
	storeVersionDirectory = "v1"
	authorityDirectory    = "v1/authority"
	authorityGenerations  = "v1/authority/generations"
	leavesDirectory       = "v1/leaves"
	currentFilename       = "current.json"
	authorityCurrent      = "current"
	authorityManifestFile = "manifest.json"
	certificateFilename   = "certificate.pem"
	privateKeyFilename    = "private-key.pem"
	maximumCertificatePEM = 64 << 10
	maximumPrivateKeyPEM  = 32 << 10
	stagingRandomBytes    = 16
)

// errActiveManifestAlreadyPublished distinguishes an atomic first-root race from ordinary rename failure.
var errActiveManifestAlreadyPublished = errors.New("active certificate manifest was concurrently published")

// writeCheckpoint identifies a durable boundary used to prove failure behavior without weakening production I/O.
type writeCheckpoint string

const (
	checkpointCertificateSynced writeCheckpoint = "certificate-synced"
	checkpointPrivateKeySynced  writeCheckpoint = "private-key-synced"
	checkpointGenerationChecked writeCheckpoint = "generation-validated"
	checkpointGenerationCommit  writeCheckpoint = "generation-commit-intent"
	checkpointGenerationMoved   writeCheckpoint = "generation-renamed"
	checkpointGenerationSynced  writeCheckpoint = "generation-directory-synced"
	checkpointManifestSynced    writeCheckpoint = "manifest-synced"
	checkpointManifestCommit    writeCheckpoint = "manifest-commit-intent"
	checkpointManifestReplaced  writeCheckpoint = "manifest-replaced"
	checkpointManifestDirSynced writeCheckpoint = "manifest-directory-synced"
)

// storeDependencies keep entropy and crash-boundary failures deterministic in package tests.
type storeDependencies struct {
	random        io.Reader
	checkpoint    func(writeCheckpoint) error
	syncDirectory func(string, *os.File) error
}

// Store owns one confined per-user certificate material directory.
type Store struct {
	mutex      sync.Mutex
	filesystem *rootedFilesystem
	random     io.Reader
	checkpoint func(writeCheckpoint) error
	closed     bool
}

// OpenDefault opens Harbor's platform-standard certificate material directory.
func OpenDefault() (*Store, error) {
	directory, err := userpaths.CertificateDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve certificate material directory: %w", err)
	}
	return Open(directory)
}

// Open creates or verifies one owner-private certificate material store.
func Open(directory string) (*Store, error) {
	return openStore(directory, storeDependencies{random: rand.Reader})
}

// openStore retains deterministic dependencies without exposing persistence fault injection to product callers.
func openStore(directory string, dependencies storeDependencies) (*Store, error) {
	if dependencies.random == nil {
		return nil, fmt.Errorf("open certificate material store: random source is required")
	}
	filesystem, err := openRootedFilesystemWithHooks(directory, nil, dependencies.syncDirectory)
	if err != nil {
		return nil, err
	}
	store := &Store{
		filesystem: filesystem,
		random:     dependencies.random,
		checkpoint: dependencies.checkpoint,
	}
	for _, path := range []string{
		storeVersionDirectory,
		authorityDirectory,
		authorityGenerations,
		leavesDirectory,
	} {
		if err := store.filesystem.ensureDirectory(filepath.FromSlash(path)); err != nil {
			return nil, errors.Join(fmt.Errorf("prepare certificate material layout: %w", err), filesystem.Close())
		}
	}
	return store, nil
}

// Close releases the rooted filesystem handle and safely tolerates repeated daemon shutdown paths.
func (store *Store) Close() error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	return store.filesystem.Close()
}

// LoadAuthority reloads the exact active CA, recovering only one unambiguous completed first-run generation.
func (store *Store) LoadAuthority(ctx context.Context, config localca.Config) (*localca.Authority, error) {
	ctx = normalizeContext(ctx)
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := store.ready(ctx); err != nil {
		return nil, err
	}
	return store.loadAuthority(ctx, config, true)
}

// CreateAuthority durably publishes a first CA without ever replacing an existing trust identity.
func (store *Store) CreateAuthority(ctx context.Context, authority *localca.Authority) error {
	ctx = normalizeContext(ctx)
	if authority == nil {
		return fmt.Errorf("create persisted certificate authority: authority is required")
	}
	if err := authority.ValidateCurrent(); err != nil {
		return fmt.Errorf("create persisted certificate authority: validate current authority: %w", err)
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := store.ready(ctx); err != nil {
		return err
	}

	material := authority.Material()
	if err := validateFingerprint(material.Fingerprint); err != nil {
		return fmt.Errorf("create persisted certificate authority: %w", err)
	}
	validationConfig := authorityValidationConfig(material)
	if _, err := localca.Load(validationConfig, material.CertificatePEM, material.PrivateKeyPEM); err != nil {
		return fmt.Errorf("create persisted certificate authority: validate material: %w", err)
	}

	current, err := store.loadAuthority(ctx, validationConfig, true)
	if err == nil {
		if current.Material().Fingerprint == material.Fingerprint {
			return nil
		}
		return ErrAuthorityAlreadyInitialized
	}
	if !errors.Is(err, ErrAuthorityNotInitialized) {
		return err
	}

	manifest := authorityManifest(material.Fingerprint)
	validate := func(certificatePEM, privateKeyPEM []byte) error {
		loaded, err := localca.Load(validationConfig, certificatePEM, privateKeyPEM)
		if err != nil {
			return err
		}
		if loaded.Material().Fingerprint != material.Fingerprint {
			return fmt.Errorf("persisted CA fingerprint does not match its generation")
		}
		return nil
	}
	if err := store.persist(ctx, filepath.FromSlash(authorityDirectory), material, manifest, false, validate); err != nil {
		if errors.Is(err, errActiveManifestAlreadyPublished) {
			return store.resolveAuthorityPublicationRace(validationConfig, material.Fingerprint)
		}
		return fmt.Errorf("create persisted certificate authority: %w", err)
	}
	loaded, err := store.loadAuthority(context.WithoutCancel(ctx), validationConfig, false)
	if err != nil {
		return fmt.Errorf("reload persisted certificate authority: %w", err)
	}
	if loaded.Material().Fingerprint != material.Fingerprint {
		return corrupt("authority generation", fmt.Errorf("reloaded fingerprint changed after publication"))
	}
	return nil
}

// LoadLeaf reloads one active exact-name leaf only when its manifest, CA, key, and SANs agree.
func (store *Store) LoadLeaf(ctx context.Context, authority *localca.Authority, hosts []string) (localca.Leaf, error) {
	ctx = normalizeContext(ctx)
	if authority == nil {
		return localca.Leaf{}, fmt.Errorf("load persisted local certificate: authority is required")
	}
	canonicalHosts, err := localca.CanonicalHosts(hosts)
	if err != nil {
		return localca.Leaf{}, fmt.Errorf("load persisted local certificate: %w", err)
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := store.ready(ctx); err != nil {
		return localca.Leaf{}, err
	}
	return store.loadLeaf(ctx, authority, canonicalHosts)
}

// PutLeaf validates and atomically activates one exact-name leaf while retaining any prior valid generation until commit.
func (store *Store) PutLeaf(ctx context.Context, authority *localca.Authority, leaf localca.Leaf) error {
	ctx = normalizeContext(ctx)
	if authority == nil {
		return fmt.Errorf("persist local certificate: authority is required")
	}
	canonicalHosts, err := localca.CanonicalHosts(leaf.Hosts)
	if err != nil {
		return fmt.Errorf("persist local certificate: %w", err)
	}
	if !slices.Equal(canonicalHosts, leaf.Hosts) {
		return fmt.Errorf("persist local certificate: hosts are not in canonical order")
	}
	validated, err := authority.LoadLeaf(leaf.Material.CertificatePEM, leaf.Material.PrivateKeyPEM, canonicalHosts)
	if err != nil {
		return fmt.Errorf("persist local certificate: validate material: %w", err)
	}
	if validated.Material.Fingerprint != leaf.Material.Fingerprint {
		return fmt.Errorf("persist local certificate: material fingerprint does not match validated certificate")
	}

	store.mutex.Lock()
	defer store.mutex.Unlock()
	if err := store.ready(ctx); err != nil {
		return err
	}
	authorityFingerprint := authority.Material().Fingerprint
	leafDirectory, err := store.ensureLeafDirectory(authorityFingerprint, canonicalHosts)
	if err != nil {
		return err
	}
	manifest := leafManifest(authorityFingerprint, leaf.Material.Fingerprint, canonicalHosts)
	validate := func(certificatePEM, privateKeyPEM []byte) error {
		loaded, err := authority.LoadLeaf(certificatePEM, privateKeyPEM, canonicalHosts)
		if err != nil {
			return err
		}
		if loaded.Material.Fingerprint != leaf.Material.Fingerprint {
			return fmt.Errorf("persisted leaf fingerprint does not match its generation")
		}
		return nil
	}
	if err := store.persist(ctx, leafDirectory, leaf.Material, manifest, true, validate); err != nil {
		return fmt.Errorf("persist local certificate: %w", err)
	}
	loaded, err := store.loadLeaf(context.WithoutCancel(ctx), authority, canonicalHosts)
	if err != nil {
		return fmt.Errorf("reload persisted local certificate: %w", err)
	}
	if loaded.Material.Fingerprint != leaf.Material.Fingerprint {
		return corrupt("leaf generation", fmt.Errorf("reloaded fingerprint changed after publication"))
	}
	return nil
}

// loadAuthority validates the active root and optionally repairs one completed but unpublished first-run generation.
func (store *Store) loadAuthority(ctx context.Context, config localca.Config, recoverGeneration bool) (*localca.Authority, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	currentPath := filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent)
	current, err := store.filesystem.openDirect(currentPath, true)
	if errors.Is(err, fs.ErrNotExist) {
		if !recoverGeneration {
			return nil, ErrAuthorityNotInitialized
		}
		return store.recoverAuthority(ctx, config)
	}
	if err != nil {
		return nil, corrupt("authority current pointer", err)
	}
	if err := current.Close(); err != nil {
		return nil, corrupt("authority current pointer", fmt.Errorf("close current directory: %w", err))
	}
	manifest, err := store.readManifest(authorityManifestPath())
	if err != nil {
		return nil, corrupt("authority manifest", err)
	}
	if err := validateAuthorityManifest(manifest); err != nil {
		return nil, corrupt("authority manifest", err)
	}
	return store.loadAuthorityGeneration(config, manifest.Fingerprint)
}

// recoverAuthority publishes only one valid orphan because choosing among multiple roots would silently change trust identity.
func (store *Store) recoverAuthority(ctx context.Context, config localca.Config) (*localca.Authority, error) {
	entries, err := store.filesystem.readDirectory(filepath.FromSlash(authorityGenerations))
	if err != nil {
		return nil, fmt.Errorf("inspect unpublished authority generations: %w", err)
	}
	fingerprints := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".staging-") {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() {
			return nil, corrupt("authority generations", fmt.Errorf("unexpected entry %q", name))
		}
		if err := validateFingerprint(name); err != nil {
			return nil, corrupt("authority generations", fmt.Errorf("generation %q: %w", name, err))
		}
		fingerprints = append(fingerprints, name)
	}
	sort.Strings(fingerprints)
	if len(fingerprints) == 0 {
		return nil, ErrAuthorityNotInitialized
	}
	if len(fingerprints) != 1 {
		return nil, corrupt("authority generations", fmt.Errorf("found %d unpublished root identities", len(fingerprints)))
	}
	authority, err := store.loadAuthorityGeneration(config, fingerprints[0])
	if err != nil {
		return nil, err
	}
	if err := store.publishManifest(ctx, filepath.FromSlash(authorityDirectory), authorityManifest(fingerprints[0]), false); err != nil {
		if errors.Is(err, errActiveManifestAlreadyPublished) {
			winner, loadErr := store.loadAuthority(ctx, config, false)
			if loadErr != nil {
				return nil, loadErr
			}
			if winner.Material().Fingerprint != fingerprints[0] {
				return nil, corrupt("authority recovery", fmt.Errorf("concurrent publication selected a different root identity"))
			}
			return winner, nil
		}
		return nil, fmt.Errorf("recover authority manifest: %w", err)
	}
	return authority, nil
}

// loadAuthorityGeneration proves the immutable pair matches both its directory name and local CA policy.
func (store *Store) loadAuthorityGeneration(config localca.Config, fingerprint string) (*localca.Authority, error) {
	generation := filepath.Join(filepath.FromSlash(authorityGenerations), fingerprint)
	certificatePEM, privateKeyPEM, err := store.readGeneration(generation)
	if err != nil {
		return nil, corrupt("authority generation", err)
	}
	authority, err := localca.Load(config, certificatePEM, privateKeyPEM)
	if err != nil {
		return nil, corrupt("authority generation", err)
	}
	if authority.Material().Fingerprint != fingerprint {
		return nil, corrupt("authority generation", fmt.Errorf("certificate fingerprint does not match generation name"))
	}
	return authority, nil
}

// loadLeaf proves one active leaf manifest and immutable pair against its retained signing authority.
func (store *Store) loadLeaf(ctx context.Context, authority *localca.Authority, hosts []string) (localca.Leaf, error) {
	if err := ctx.Err(); err != nil {
		return localca.Leaf{}, err
	}
	authorityFingerprint := authority.Material().Fingerprint
	directory := leafDirectory(authorityFingerprint, hosts)
	manifest, err := store.readManifest(filepath.Join(directory, currentFilename))
	if errors.Is(err, fs.ErrNotExist) {
		return localca.Leaf{}, ErrLeafNotFound
	}
	if err != nil {
		return localca.Leaf{}, corrupt("leaf manifest", err)
	}
	if err := validateLeafManifest(manifest, authorityFingerprint, hosts); err != nil {
		return localca.Leaf{}, corrupt("leaf manifest", err)
	}
	generation := filepath.Join(directory, "generations", manifest.Fingerprint)
	certificatePEM, privateKeyPEM, err := store.readGeneration(generation)
	if err != nil {
		return localca.Leaf{}, corrupt("leaf generation", err)
	}
	leaf, err := authority.LoadLeaf(certificatePEM, privateKeyPEM, hosts)
	if err != nil {
		return localca.Leaf{}, corrupt("leaf generation", err)
	}
	if leaf.Material.Fingerprint != manifest.Fingerprint {
		return localca.Leaf{}, corrupt("leaf generation", fmt.Errorf("certificate fingerprint does not match generation name"))
	}
	return leaf, nil
}

// ensureLeafDirectory creates the CA- and SAN-scoped hierarchy before any private material enters it.
func (store *Store) ensureLeafDirectory(authorityFingerprint string, hosts []string) (string, error) {
	if err := validateFingerprint(authorityFingerprint); err != nil {
		return "", fmt.Errorf("prepare leaf directory: authority fingerprint: %w", err)
	}
	directory := leafDirectory(authorityFingerprint, hosts)
	for _, path := range []string{
		filepath.Join(filepath.FromSlash(leavesDirectory), authorityFingerprint),
		directory,
		filepath.Join(directory, "generations"),
	} {
		if err := store.filesystem.ensureDirectory(path); err != nil {
			return "", fmt.Errorf("prepare leaf directory: %w", err)
		}
	}
	return directory, nil
}

// leafDirectory derives a path only from previously validated cryptographic identifiers.
func leafDirectory(authorityFingerprint string, hosts []string) string {
	return filepath.Join(filepath.FromSlash(leavesDirectory), authorityFingerprint, hostSetID(hosts))
}

// authorityManifestPath identifies the file inside the atomically installed nonempty authority pointer directory.
func authorityManifestPath() string {
	return filepath.Join(filepath.FromSlash(authorityDirectory), authorityCurrent, authorityManifestFile)
}

// persist stages, validates, and activates one pair with the manifest replacement as its commit point.
func (store *Store) persist(
	ctx context.Context,
	directory string,
	material localca.Material,
	manifest activeManifest,
	replaceManifest bool,
	validate func([]byte, []byte) error,
) (persistErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if validate == nil {
		return fmt.Errorf("persist certificate material: generation validator is required")
	}
	if err := validateFingerprint(material.Fingerprint); err != nil {
		return fmt.Errorf("persist certificate material: %w", err)
	}
	if manifest.Fingerprint != material.Fingerprint {
		return fmt.Errorf("persist certificate material: manifest and certificate fingerprints differ")
	}
	if len(material.CertificatePEM) == 0 || len(material.CertificatePEM) > maximumCertificatePEM {
		return fmt.Errorf("persist certificate material: certificate encoding exceeds policy")
	}
	if len(material.PrivateKeyPEM) == 0 || len(material.PrivateKeyPEM) > maximumPrivateKeyPEM {
		return fmt.Errorf("persist certificate material: private-key encoding exceeds policy")
	}

	generations := filepath.Join(directory, "generations")
	if err := store.filesystem.ensureDirectory(generations); err != nil {
		return err
	}
	stagingName, err := store.stagingName()
	if err != nil {
		return err
	}
	staging := filepath.Join(generations, stagingName)
	if err := store.filesystem.ensureDirectory(staging); err != nil {
		return fmt.Errorf("create certificate staging directory: %w", err)
	}
	defer func() {
		if err := store.filesystem.removeAll(staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
			persistErr = errors.Join(persistErr, fmt.Errorf("remove certificate staging directory: %w", err))
		}
	}()

	if err := store.filesystem.writeExclusiveFile(filepath.Join(staging, certificateFilename), material.CertificatePEM); err != nil {
		return err
	}
	if err := store.reach(checkpointCertificateSynced); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(staging, privateKeyFilename), material.PrivateKeyPEM); err != nil {
		return err
	}
	if err := store.reach(checkpointPrivateKeySynced); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	certificatePEM, privateKeyPEM, err := store.readGeneration(staging)
	if err != nil {
		return fmt.Errorf("validate staged certificate generation: %w", err)
	}
	if err := validate(certificatePEM, privateKeyPEM); err != nil {
		return fmt.Errorf("validate staged certificate generation: %w", err)
	}
	if err := store.reach(checkpointGenerationChecked); err != nil {
		return err
	}
	if err := store.filesystem.syncDirectoryPath(staging); err != nil {
		return fmt.Errorf("sync staged certificate generation: %w", err)
	}
	if err := store.reach(checkpointGenerationCommit); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	generation := filepath.Join(generations, material.Fingerprint)
	if _, err := store.filesystem.lstat(generation); err == nil {
		existingCertificate, existingKey, readErr := store.readGeneration(generation)
		if readErr != nil {
			return corrupt("existing immutable generation", readErr)
		}
		if err := validate(existingCertificate, existingKey); err != nil {
			return corrupt("existing immutable generation", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect immutable certificate generation: %w", err)
	} else if err := store.filesystem.rename(staging, generation, false); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("publish immutable certificate generation: %w", err)
		}
		existingCertificate, existingKey, readErr := store.readGeneration(generation)
		if readErr != nil {
			return corrupt("concurrently published immutable generation", readErr)
		}
		if validateErr := validate(existingCertificate, existingKey); validateErr != nil {
			return corrupt("concurrently published immutable generation", validateErr)
		}
	}
	if err := store.reach(checkpointGenerationMoved); err != nil {
		return err
	}
	if err := store.filesystem.syncDirectoryPath(generations); err != nil {
		return fmt.Errorf("sync certificate generations: %w", err)
	}
	if err := store.reach(checkpointGenerationSynced); err != nil {
		return err
	}
	return store.publishManifest(context.WithoutCancel(ctx), directory, manifest, replaceManifest)
}

// publishManifest atomically selects a completed generation after its strict pointer is durably staged.
func (store *Store) publishManifest(ctx context.Context, directory string, manifest activeManifest, replace bool) (publishErr error) {
	if !replace {
		return store.publishInitialManifest(ctx, directory, manifest)
	}
	encoded, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	temporaryName, err := store.stagingName()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, temporaryName+".json")
	defer func() {
		if err := store.filesystem.removeAll(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
			publishErr = errors.Join(publishErr, fmt.Errorf("remove temporary certificate manifest: %w", err))
		}
	}()
	if err := store.filesystem.writeExclusiveFile(temporary, encoded); err != nil {
		return fmt.Errorf("stage active certificate manifest: %w", err)
	}
	if err := store.reach(checkpointManifestSynced); err != nil {
		return err
	}
	if err := store.reach(checkpointManifestCommit); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.filesystem.rename(temporary, filepath.Join(directory, currentFilename), true); err != nil {
		return fmt.Errorf("replace active certificate manifest: %w", err)
	}
	if err := store.reach(checkpointManifestReplaced); err != nil {
		return err
	}
	if err := store.filesystem.syncDirectoryPath(directory); err != nil {
		return fmt.Errorf("sync active certificate manifest directory: %w", err)
	}
	return store.reach(checkpointManifestDirSynced)
}

// publishInitialManifest atomically installs a nonempty directory so same-root rename cannot replace a concurrent winner.
func (store *Store) publishInitialManifest(ctx context.Context, directory string, manifest activeManifest) (publishErr error) {
	encoded, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	temporaryName, err := store.stagingName()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, temporaryName+"-current")
	defer func() {
		if err := store.filesystem.removeAll(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
			publishErr = errors.Join(publishErr, fmt.Errorf("remove temporary authority manifest directory: %w", err))
		}
	}()
	if err := store.filesystem.ensureDirectory(temporary); err != nil {
		return fmt.Errorf("stage authority manifest directory: %w", err)
	}
	if err := store.filesystem.writeExclusiveFile(filepath.Join(temporary, authorityManifestFile), encoded); err != nil {
		return fmt.Errorf("stage authority manifest: %w", err)
	}
	if err := store.reach(checkpointManifestSynced); err != nil {
		return err
	}
	if err := store.filesystem.syncDirectoryPath(temporary); err != nil {
		return fmt.Errorf("sync staged authority manifest directory: %w", err)
	}
	if err := store.reach(checkpointManifestCommit); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.filesystem.rename(temporary, filepath.Join(directory, authorityCurrent), false); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errors.Join(errActiveManifestAlreadyPublished, err)
		}
		return fmt.Errorf("install active authority manifest: %w", err)
	}
	if err := store.reach(checkpointManifestReplaced); err != nil {
		return err
	}
	if err := store.filesystem.syncDirectoryPath(directory); err != nil {
		return fmt.Errorf("sync authority manifest directory: %w", err)
	}
	return store.reach(checkpointManifestDirSynced)
}

// resolveAuthorityPublicationRace preserves idempotence only when the first writer selected the exact same root identity.
func (store *Store) resolveAuthorityPublicationRace(config localca.Config, fingerprint string) error {
	manifest, err := store.readManifest(authorityManifestPath())
	if err != nil {
		return corrupt("concurrently published authority manifest", err)
	}
	if err := validateAuthorityManifest(manifest); err != nil {
		return corrupt("concurrently published authority manifest", err)
	}
	if manifest.Fingerprint != fingerprint {
		return ErrAuthorityAlreadyInitialized
	}
	authority, err := store.loadAuthorityGeneration(config, fingerprint)
	if err != nil {
		return err
	}
	if authority.Material().Fingerprint != fingerprint {
		return corrupt("concurrently published authority generation", fmt.Errorf("reloaded fingerprint changed after publication"))
	}
	return nil
}

// readManifest applies the size bound before strict schema parsing.
func (store *Store) readManifest(path string) (activeManifest, error) {
	encoded, err := store.filesystem.readBoundedFile(path, maximumManifestBytes)
	if err != nil {
		return activeManifest{}, err
	}
	return decodeManifest(encoded)
}

// readGeneration returns owned bounded bytes only after the directory contains exactly one certificate and key.
func (store *Store) readGeneration(directory string) ([]byte, []byte, error) {
	if err := store.filesystem.validateGenerationDirectory(directory); err != nil {
		return nil, nil, err
	}
	certificatePEM, err := store.filesystem.readBoundedFile(filepath.Join(directory, certificateFilename), maximumCertificatePEM)
	if err != nil {
		return nil, nil, err
	}
	privateKeyPEM, err := store.filesystem.readBoundedFile(filepath.Join(directory, privateKeyFilename), maximumPrivateKeyPEM)
	if err != nil {
		return nil, nil, err
	}
	return certificatePEM, privateKeyPEM, nil
}

// stagingName creates an unguessable internal name that cannot collide with a generation fingerprint.
func (store *Store) stagingName() (string, error) {
	random := make([]byte, stagingRandomBytes)
	if _, err := io.ReadFull(store.random, random); err != nil {
		return "", fmt.Errorf("create certificate staging name: %w", err)
	}
	return ".staging-" + hex.EncodeToString(random), nil
}

// reach invokes a test-only failure boundary without changing production persistence behavior.
func (store *Store) reach(checkpoint writeCheckpoint) error {
	if store.checkpoint == nil {
		return nil
	}
	if err := store.checkpoint(checkpoint); err != nil {
		return fmt.Errorf("certificate persistence checkpoint %q: %w", checkpoint, err)
	}
	return nil
}

// ready checks cancellation and lifecycle before an operation touches durable state.
func (store *Store) ready(ctx context.Context) error {
	if store.closed {
		return ErrStoreClosed
	}
	return ctx.Err()
}

// normalizeContext gives nil callers the same cancellation-free semantics as other Harbor boundaries.
func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// authorityValidationConfig reloads a caller-validated root at an instant strictly inside its encoded lifetime.
func authorityValidationConfig(material localca.Material) localca.Config {
	validationTime := material.NotBefore.Add(time.Second)
	return localca.Config{Now: func() time.Time { return validationTime }}
}
