package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
)

// gateProof correlates a live native App init with this invocation's private marker.
type gateProof struct {
	Address string `json:"address"`
	Marker  string `json:"marker"`
}

// validateGateProof rejects stale markers and endpoints outside the fixed IPv4 loopback boundary.
func validateGateProof(proof gateProof, marker string) error {
	host, port, err := net.SplitHostPort(proof.Address)
	if err != nil {
		return err
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || host != "127.0.0.1" || proof.Marker != marker || marker == "" {
		return fmt.Errorf("gate proof does not identify this run's loopback listener")
	}
	return nil
}

// readGateProof bounds untrusted fixture output before decoding its private run correlation.
func readGateProof(path, marker string) (gateProof, error) {
	var proof gateProof
	file, err := os.Open(path)
	if err != nil {
		return proof, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return proof, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2048 {
		return proof, fmt.Errorf("gate proof is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 2049))
	if err != nil {
		return proof, err
	}
	if len(data) > 2048 {
		return proof, fmt.Errorf("gate proof exceeds bound")
	}
	if err := json.Unmarshal(data, &proof); err != nil {
		return proof, err
	}
	return proof, validateGateProof(proof, marker)
}

// gateSource generates a native init gate without shell hooks or product configuration overrides.
func gateSource(parent, marker string) string {
	return fmt.Sprintf(`package main

import (
 "encoding/json"
 "fmt"
 "net"
 "net/http"
 "os"
 "path/filepath"
 "time"
)

// init blocks only this disposable peer when its private recovery marker is enabled.
func init() {
 parent, marker := %q, %q
 enabled := filepath.Join(parent, "gate-enabled")
 data, err := os.ReadFile(enabled)
 if os.IsNotExist(err) { return }
 if err != nil || string(data) != marker { panic("invalid recovery gate enable marker") }
 listener, err := net.Listen("tcp4", "127.0.0.1:0")
 if err != nil { panic(err) }
 server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, marker) })}
 defer server.Close()
 go server.Serve(listener)
 proof, err := json.Marshal(struct { Address string `+"`json:\"address\"`"+`; Marker string `+"`json:\"marker\"`"+` }{listener.Addr().String(), marker})
 if err != nil { panic(err) }
 temporary := filepath.Join(parent, "entered.json.tmp")
 if err := os.WriteFile(temporary, proof, 0600); err != nil { panic(err) }
 if err := os.Rename(temporary, filepath.Join(parent, "entered.json")); err != nil { panic(err) }
 deadline := time.Now().Add(10*time.Minute)
 for time.Now().Before(deadline) {
  data, err := os.ReadFile(enabled)
  if os.IsNotExist(err) { return }
  if err != nil || string(data) != marker { panic("recovery gate marker changed") }
  if data, err := os.ReadFile(filepath.Join(parent, "gate-release")); err == nil && string(data) == marker { return }
  time.Sleep(100*time.Millisecond)
 }
 panic("recovery gate deadline expired")
}
`, parent, marker)
}
