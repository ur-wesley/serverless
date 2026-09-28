package cli

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// ZipDir zips dir (skips .git, node_modules, data) for upload.
func ZipDir(dir string) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if d.IsDir() {
			if base == ".git" || base == "node_modules" || base == "data" {
				return filepath.SkipDir
			}
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		fw, err := w.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type DeployResult struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Image   string `json:"image"`
	SHA256  string `json:"sha256"`
}

func Deploy(baseURL, name, configTOML string, srcZip []byte) (*DeployResult, error) {
	body, _ := json.Marshal(map[string]string{
		"name": name, "config_toml": configTOML,
		"src_zip_b64": base64.StdEncoding.EncodeToString(srcZip),
	})
	resp, err := httpClient.Post(strings.TrimSuffix(baseURL, "/")+"/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("deploy: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out DeployResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type InvokeResult struct {
	Status  int
	Headers http.Header
	Body    []byte
}

func Invoke(baseURL, fnPath, method string, body []byte) (*InvokeResult, error) {
	if !strings.HasPrefix(fnPath, "/") {
		fnPath = "/" + fnPath
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(baseURL, "/")+"/f/"+strings.TrimPrefix(fnPath, "/"), rdr)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return &InvokeResult{Status: resp.StatusCode, Headers: resp.Header, Body: raw}, nil
}

type Function struct {
	Name          string         `json:"Name"`
	ActiveVersion string         `json:"ActiveVersion"`
	ConfigTOML    string         `json:"ConfigTOML"`
	DeployedAt    string         `json:"DeployedAt"`
	Warm          []WarmInstance `json:"Warm"`
}

// WarmInstance mirrors runner.InstanceStatus on the wire.
type WarmInstance struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Image     string    `json:"image"`
	Container string    `json:"container"`
	StartedAt time.Time `json:"startedAt"`
	LastUsed  time.Time `json:"lastUsed"`
}

func ListFunctions(baseURL string) ([]Function, error) {
	resp, err := httpClient.Get(strings.TrimSuffix(baseURL, "/") + "/functions")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []Function
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

type LogLine struct {
	Function  string    `json:"Function"`
	Version   string    `json:"Version"`
	RequestID string    `json:"RequestID"`
	Line      string    `json:"Line"`
	Time      time.Time `json:"Time"`
}

func GetLogs(baseURL, fn string) ([]LogLine, error) {
	resp, err := httpClient.Get(strings.TrimSuffix(baseURL, "/") + "/logs?fn=" + fn)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16 * 1024))
		return nil, fmt.Errorf("logs: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out []LogLine
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
