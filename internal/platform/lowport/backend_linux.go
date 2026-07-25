//go:build linux

package lowport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	ubuntuNFTExecutablePath      = "/usr/sbin/nft"
	ubuntuNFTExecutableFDPath    = "/proc/self/fd/3"
	ubuntuNFTRulesetFDPath       = "/proc/self/fd/4"
	ubuntuNFTCommandTimeout      = 10 * time.Second
	ubuntuNFTObservationRetries  = 4
	ubuntuNFTObservationDelay    = 10 * time.Millisecond
	maximumUbuntuNFTOutput       = 128 << 10
	maximumUbuntuNFTTableRecords = 256
)

// ubuntuNFTSystem owns only Harbor's compiled fixed inet table.
type ubuntuNFTSystem struct{}

// ubuntuNFTCommand identifies one fixed argv and optional ruleset input.
type ubuntuNFTCommand string

const (
	ubuntuNFTCommandListTables ubuntuNFTCommand = "list-tables"
	ubuntuNFTCommandListTable  ubuntuNFTCommand = "list-table"
	ubuntuNFTCommandEnsure     ubuntuNFTCommand = "ensure"
	ubuntuNFTCommandRelease    ubuntuNFTCommand = "release"
)

// ubuntuNFTCommandResult contains bounded child output and completion evidence.
type ubuntuNFTCommandResult struct {
	stdout []byte
	stderr []byte
}

// ubuntuNFTProcessResult retains the narrow completion evidence needed by the fixed runner.
type ubuntuNFTProcessResult struct {
	state *os.ProcessState
	err   error
}

// ubuntuNFTTablesDocument is the bounded subset of nft's JSON list-tables response.
type ubuntuNFTTablesDocument struct {
	NFTables []json.RawMessage `json:"nftables"`
}

// ubuntuNFTTableEnvelope identifies one table entry while ignoring the fixed command's metainfo record.
type ubuntuNFTTableEnvelope struct {
	Table *ubuntuNFTTableIdentity `json:"table"`
}

// ubuntuNFTTableIdentity contains only the namespace needed to find Harbor's fixed table.
type ubuntuNFTTableIdentity struct {
	Family string `json:"family"`
	Name   string `json:"name"`
}

// New constructs the reviewed Ubuntu nftables low-port adapter.
func New() (*Adapter, error) {
	return newAdapter(newUbuntuNFTBackend(ubuntuNFTSystem{})), nil
}

// snapshot observes table existence independently before reading its bounded canonical rules.
func (ubuntuNFTSystem) snapshot(ctx context.Context, request Request) (ubuntuNFTSnapshot, error) {
	if err := validateUbuntuNFTRequest(request); err != nil {
		return ubuntuNFTSnapshot{}, err
	}
	tables, count, err := observeUbuntuNFTTables(ctx)
	if err != nil {
		return ubuntuNFTSnapshot{}, err
	}
	if count == 0 {
		return ubuntuNFTSnapshot{}, nil
	}
	if count != 1 {
		fingerprint := ubuntuNFTContentFingerprint("ambiguous-table-list", string(tables.stdout))
		return ubuntuNFTSnapshot{
			TablePresent:     true,
			TableAmbiguous:   true,
			TableFingerprint: fingerprint,
			RulesPresent:     true,
			RulesAmbiguous:   true,
			RulesFingerprint: fingerprint,
		}, nil
	}
	return observeStableUbuntuNFTTable(ctx, request)
}

// observeUbuntuNFTTables retries only the read-only namespace snapshot when nft returns incomplete output around a transaction.
func observeUbuntuNFTTables(ctx context.Context) (ubuntuNFTCommandResult, int, error) {
	ctx = normalizedContext(ctx)
	var lastResult ubuntuNFTCommandResult
	var lastErr error
	for attempt := 0; attempt < ubuntuNFTObservationRetries; attempt++ {
		result, err := runUbuntuNFTCommand(ctx, ubuntuNFTCommandListTables, nil)
		if err == nil {
			count, decodeErr := countUbuntuNFTTables(result.stdout)
			if decodeErr == nil {
				return result, count, nil
			}
			err = decodeErr
		}
		lastResult = result
		lastErr = err
		if attempt+1 == ubuntuNFTObservationRetries {
			break
		}
		timer := time.NewTimer(ubuntuNFTObservationDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return lastResult, 0, ctx.Err()
		case <-timer.C:
		}
	}
	return lastResult, 0, lastErr
}

// observeStableUbuntuNFTTable requires two matching read-only snapshots so partial post-transaction output cannot authorize mutation.
func observeStableUbuntuNFTTable(ctx context.Context, request Request) (ubuntuNFTSnapshot, error) {
	ctx = normalizedContext(ctx)
	var previous ubuntuNFTSnapshot
	previousFound := false
	var lastErr error
	for attempt := 0; attempt < ubuntuNFTObservationRetries; attempt++ {
		listed, err := runUbuntuNFTCommand(ctx, ubuntuNFTCommandListTable, nil)
		if err == nil {
			current, parseErr := parseUbuntuNFTTable(request, listed.stdout)
			if parseErr == nil {
				if previousFound && current == previous {
					return current, nil
				}
				previous = current
				previousFound = true
			} else {
				err = parseErr
			}
		}
		lastErr = err
		if attempt+1 == ubuntuNFTObservationRetries {
			break
		}
		timer := time.NewTimer(ubuntuNFTObservationDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ubuntuNFTSnapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
	if previousFound {
		return ubuntuNFTSnapshot{}, errors.New("nftables fixed table observation did not stabilize")
	}
	return ubuntuNFTSnapshot{}, lastErr
}

// ensure creates the complete fixed table through one atomic nftables transaction.
func (system ubuntuNFTSystem) ensure(ctx context.Context, request Request) error {
	if err := validateUbuntuNFTRequest(request); err != nil {
		return err
	}
	before, err := system.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if before.TablePresent || before.RulesPresent {
		return fmt.Errorf("Ubuntu nftables table changed before ensure: %w", errNativeMutationConflict)
	}
	_, err = runUbuntuNFTCommand(ctx, ubuntuNFTCommandEnsure, []byte(ubuntuNFTRuleset(request)))
	return err
}

// release revalidates the exact fixed table immediately before deleting its namespace.
func (system ubuntuNFTSystem) release(ctx context.Context, request Request) error {
	if err := validateUbuntuNFTRequest(request); err != nil {
		return err
	}
	before, err := system.snapshot(ctx, request)
	if err != nil {
		return err
	}
	if !before.TableExact || !before.RulesExact || before.TableAmbiguous || before.RulesAmbiguous {
		return fmt.Errorf("Ubuntu nftables table changed before release: %w", errNativeMutationConflict)
	}
	_, err = runUbuntuNFTCommand(ctx, ubuntuNFTCommandRelease, nil)
	return err
}

// countUbuntuNFTTables returns the exact number of fixed-family and fixed-name table entries.
func countUbuntuNFTTables(content []byte) (int, error) {
	var document ubuntuNFTTablesDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return 0, fmt.Errorf("decode nftables table list: %w", err)
	}
	var trailing any
	if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) ||
		len(document.NFTables) > maximumUbuntuNFTTableRecords {
		return 0, errors.New("nftables table list is oversized or contains trailing data")
	}
	count := 0
	for _, record := range document.NFTables {
		var envelope ubuntuNFTTableEnvelope
		if err := json.Unmarshal(record, &envelope); err != nil {
			return 0, fmt.Errorf("decode nftables table record: %w", err)
		}
		if envelope.Table != nil && envelope.Table.Family == "inet" && envelope.Table.Name == ubuntuNFTTableName {
			count++
		}
	}
	return count, nil
}

// parseUbuntuNFTTable classifies fixed table ownership and exact rule text from bounded native output.
func parseUbuntuNFTTable(request Request, content []byte) (ubuntuNFTSnapshot, error) {
	if !utf8SafeUbuntuNFTOutput(content) {
		return ubuntuNFTSnapshot{}, errors.New("nftables table output is not bounded canonical UTF-8")
	}
	lines := normalizeUbuntuNFTLines(content)
	if len(lines) == 0 {
		return ubuntuNFTSnapshot{}, errors.New("nftables fixed table output is empty")
	}
	expected := normalizeUbuntuNFTLines([]byte(ubuntuNFTListedTable(request)))
	ownerLine := `comment "` + ubuntuNFTOwnerComment(request) + `"`
	httpComment := `comment "` + ubuntuNFTHTTPComment + `"`
	httpsComment := `comment "` + ubuntuNFTHTTPSComment + `"`
	tableCommentCount := slices.Index(lines, ownerLine)
	httpCount := countUbuntuNFTLineSuffix(lines, httpComment)
	httpsCount := countUbuntuNFTLineSuffix(lines, httpsComment)
	chainCount := countUbuntuNFTExactLine(lines, "chain "+ubuntuNFTChainName+" {")
	tableOwned := tableCommentCount >= 0
	rulesOwned := tableOwned && httpCount == 1 && httpsCount == 1
	exact := slices.Equal(lines, expected)
	tablePayload := strings.Join(ubuntuNFTTableFactLines(lines), "\n")
	rulesPayload := strings.Join(lines, "\n")
	return ubuntuNFTSnapshot{
		TablePresent:     true,
		TableOwned:       tableOwned,
		TableExact:       exact && tableOwned,
		TableAmbiguous:   countUbuntuNFTExactLine(lines, ownerLine) > 1,
		TableFingerprint: ubuntuNFTContentFingerprint("table", tablePayload),
		RulesPresent:     true,
		RulesOwned:       rulesOwned,
		RulesExact:       exact && rulesOwned,
		RulesAmbiguous:   chainCount != 1 || httpCount > 1 || httpsCount > 1,
		RulesFingerprint: ubuntuNFTContentFingerprint("rules", rulesPayload),
	}, nil
}

// ubuntuNFTRuleset returns the sole declarative transaction Harbor may add.
func ubuntuNFTRuleset(request Request) string {
	return fmt.Sprintf(`table inet %s {
	comment "%s"
	chain %s {
		type nat hook output priority dstnat; policy accept;
		ip daddr %s tcp dport 80 redirect to :%d comment "%s"
		ip daddr %s tcp dport 443 redirect to :%d comment "%s"
	}
}
`,
		ubuntuNFTTableName,
		ubuntuNFTOwnerComment(request),
		ubuntuNFTChainName,
		request.LoopbackPool(),
		request.HTTPUpstream().Port(),
		ubuntuNFTHTTPComment,
		request.LoopbackPool(),
		request.HTTPSUpstream().Port(),
		ubuntuNFTHTTPSComment,
	)
}

// ubuntuNFTListedTable returns the normalized Ubuntu nft list spelling expected after creation.
func ubuntuNFTListedTable(request Request) string {
	return strings.Replace(ubuntuNFTRuleset(request), "priority dstnat", "priority -100", 1)
}

// runUbuntuNFTCommand executes one allowlisted nft argv against a retained executable inode.
func runUbuntuNFTCommand(ctx context.Context, operation ubuntuNFTCommand, ruleset []byte) (ubuntuNFTCommandResult, error) {
	arguments, needsRuleset, err := ubuntuNFTArguments(operation)
	if err != nil {
		return ubuntuNFTCommandResult{}, err
	}
	if needsRuleset != (ruleset != nil) || needsRuleset && len(ruleset) > maximumUbuntuNFTOutput {
		return ubuntuNFTCommandResult{}, errors.New("nftables command ruleset input does not match its fixed operation")
	}
	executable, err := openUbuntuNFTExecutable()
	if err != nil {
		return ubuntuNFTCommandResult{}, err
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = executable.Close()
		return ubuntuNFTCommandResult{}, err
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = executable.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return ubuntuNFTCommandResult{}, err
	}
	files := []*os.File{nil, stdoutWriter, stderrWriter, executable}
	var rulesetFile *os.File
	if needsRuleset {
		rulesetFile, err = ubuntuNFTRulesetFile(ruleset)
		if err != nil {
			_ = executable.Close()
			_ = stdoutReader.Close()
			_ = stdoutWriter.Close()
			_ = stderrReader.Close()
			_ = stderrWriter.Close()
			return ubuntuNFTCommandResult{}, err
		}
		files = append(files, rulesetFile)
	}
	process, err := os.StartProcess(ubuntuNFTExecutableFDPath, append([]string{"nft"}, arguments...), &os.ProcAttr{
		Dir:   "/",
		Env:   []string{"LANG=C", "LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"},
		Files: files,
	})
	closeErr := errors.Join(stdoutWriter.Close(), stderrWriter.Close(), executable.Close())
	if rulesetFile != nil {
		closeErr = errors.Join(closeErr, rulesetFile.Close())
	}
	if err != nil {
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
		return ubuntuNFTCommandResult{}, errors.Join(err, closeErr)
	}
	stdout := &ubuntuNFTBuffer{}
	stderr := &ubuntuNFTBuffer{}
	stdoutResult := readUbuntuNFTPipe(stdoutReader, stdout)
	stderrResult := readUbuntuNFTPipe(stderrReader, stderr)
	waitResult := make(chan ubuntuNFTProcessResult, 1)
	go func() {
		state, waitErr := process.Wait()
		waitResult <- ubuntuNFTProcessResult{state: state, err: waitErr}
	}()
	commandContext, cancel := context.WithTimeout(normalizedContext(ctx), ubuntuNFTCommandTimeout)
	defer cancel()
	var processResult ubuntuNFTProcessResult
	select {
	case processResult = <-waitResult:
	case <-commandContext.Done():
		killErr := process.Kill()
		processResult = <-waitResult
		processResult.err = errors.Join(commandContext.Err(), killErr, processResult.err)
	}
	result := ubuntuNFTCommandResult{
		stdout: slices.Clone(stdout.bytes),
		stderr: slices.Clone(stderr.bytes),
	}
	runErr := errors.Join(closeErr, <-stdoutResult, <-stderrResult, processResult.err)
	if runErr == nil && (processResult.state == nil || !processResult.state.Success()) {
		if processResult.state == nil {
			runErr = errors.New("nft exited without process state")
		} else {
			runErr = fmt.Errorf("nft exited with %s", processResult.state.String())
		}
	}
	if runErr != nil {
		message := strings.TrimSpace(string(result.stderr))
		if len(message) > 512 {
			message = message[:512]
		}
		return result, fmt.Errorf("execute nft %s: %w: %s", operation, runErr, message)
	}
	return result, nil
}

// ubuntuNFTArguments returns the immutable argv and input shape for one reviewed operation.
func ubuntuNFTArguments(operation ubuntuNFTCommand) ([]string, bool, error) {
	switch operation {
	case ubuntuNFTCommandListTables:
		return []string{"-j", "list", "tables"}, false, nil
	case ubuntuNFTCommandListTable:
		return []string{"-nn", "list", "table", "inet", ubuntuNFTTableName}, false, nil
	case ubuntuNFTCommandEnsure:
		return []string{"-f", ubuntuNFTRulesetFDPath}, true, nil
	case ubuntuNFTCommandRelease:
		return []string{"delete", "table", "inet", ubuntuNFTTableName}, false, nil
	default:
		return nil, false, fmt.Errorf("nftables operation %q is unsupported", operation)
	}
}

// openUbuntuNFTExecutable retains the reviewed root-owned executable inode across launch.
func openUbuntuNFTExecutable() (*os.File, error) {
	descriptor, err := unix.Open(ubuntuNFTExecutablePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), ubuntuNFTExecutablePath)
	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Uid != 0 || status.Gid != 0 || status.Mode&0o022 != 0 {
		return nil, errors.Join(errors.New("Ubuntu nft executable is not root-owned and protected"), file.Close())
	}
	return file, nil
}

// ubuntuNFTRulesetFile publishes immutable input through one sealed anonymous descriptor.
func ubuntuNFTRulesetFile(content []byte) (*os.File, error) {
	descriptor, err := unix.MemfdCreate("harbor-nft-ruleset", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), "harbor-nft-ruleset")
	_, writeErr := file.Write(content)
	_, sealErr := unix.FcntlInt(
		file.Fd(),
		unix.F_ADD_SEALS,
		unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE,
	)
	_, seekErr := file.Seek(0, io.SeekStart)
	if err := errors.Join(writeErr, sealErr, seekErr); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// readUbuntuNFTPipe drains one child stream concurrently into a bounded buffer.
func readUbuntuNFTPipe(reader *os.File, buffer *ubuntuNFTBuffer) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(buffer, reader)
		result <- errors.Join(copyErr, reader.Close())
	}()
	return result
}

// ubuntuNFTBuffer rejects native output beyond the fixed parser budget.
type ubuntuNFTBuffer struct {
	bytes []byte
}

// Write retains bounded native output or stops the child stream on overflow.
func (buffer *ubuntuNFTBuffer) Write(content []byte) (int, error) {
	if len(buffer.bytes)+len(content) > maximumUbuntuNFTOutput {
		return 0, fmt.Errorf("nftables output exceeds %d bytes", maximumUbuntuNFTOutput)
	}
	buffer.bytes = append(buffer.bytes, content...)
	return len(content), nil
}

// normalizeUbuntuNFTLines collapses formatting whitespace while retaining every semantic token.
func normalizeUbuntuNFTLines(content []byte) []string {
	raw := strings.Split(strings.TrimSpace(string(content)), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		normalized := strings.Join(strings.Fields(line), " ")
		if normalized != "" {
			lines = append(lines, normalized)
		}
	}
	return lines
}

// countUbuntuNFTExactLine counts one complete normalized line.
func countUbuntuNFTExactLine(lines []string, expected string) int {
	count := 0
	for _, line := range lines {
		if line == expected {
			count++
		}
	}
	return count
}

// countUbuntuNFTLineSuffix counts rules carrying one exact terminal owner comment.
func countUbuntuNFTLineSuffix(lines []string, suffix string) int {
	count := 0
	for _, line := range lines {
		if strings.HasSuffix(line, suffix) {
			count++
		}
	}
	return count
}

// ubuntuNFTTableFactLines retains the fixed table header and every table-level comment.
func ubuntuNFTTableFactLines(lines []string) []string {
	facts := []string{lines[0]}
	for _, line := range lines {
		if strings.HasPrefix(line, `comment "`) {
			facts = append(facts, line)
		}
	}
	return facts
}

// ubuntuNFTContentFingerprint binds one observed native text subset to its artifact namespace.
func ubuntuNFTContentFingerprint(kind string, content string) string {
	digest := sha256.Sum256([]byte("goforj.harbor.ubuntu-nftables." + kind + ".v1\x00" + content))
	return hex.EncodeToString(digest[:])
}

// utf8SafeUbuntuNFTOutput rejects control bytes outside the fixed textual nft response.
func utf8SafeUbuntuNFTOutput(content []byte) bool {
	if len(content) == 0 || len(content) > maximumUbuntuNFTOutput || !utf8.Valid(content) {
		return false
	}
	for _, value := range content {
		if value == 0 {
			return false
		}
	}
	return true
}
