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

// ZipDir zips dir (skips .git, node_modules, data, build outputs) for upload.
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
			if base == ".git" || base == "node_modules" || base == "data" || base == ".beads" {
				return filepath.SkipDir
			}
			return nil
		}
		if base == "controlplane.exe" || base == "controlplane" || base == "actions.exe" || strings.HasSuffix(base, ".db") {
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

// DeployJob mirrors store.Job over the jobs API.
type DeployJob struct {
	ID        string `json:"id"`
	FnName    string `json:"fn_name"`
	Status    string `json:"status"`
	Version   string `json:"version"`
	Image     string `json:"image"`
	SHA256    string `json:"sha256"`
	Error     string `json:"error"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func deployReq(baseURL, token string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest("POST", strings.TrimSuffix(baseURL, "/")+"/deploy", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// EnqueueDeploy uploads src and returns the queued job (HTTP 202).
func EnqueueDeploy(baseURL, token, name, configTOML string, srcZip []byte) (*DeployJob, error) {
	body, _ := json.Marshal(map[string]string{
		"name": name, "config_toml": configTOML,
		"src_zip_b64": base64.StdEncoding.EncodeToString(srcZip),
	})
	req, err := deployReq(baseURL, token, body)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("deploy: unauthorized (run `oort login`)")
	}
	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("deploy: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out DeployJob
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDeployJob fetches one job by id.
func GetDeployJob(baseURL, token, id string) (*DeployJob, error) {
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/deploys/"+id, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("job %s: %d: %s", id, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out DeployJob
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDeployJobs lists a function's jobs, newest first.
func ListDeployJobs(baseURL, token, fn string) ([]DeployJob, error) {
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/deploys?fn="+fn, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []DeployJob
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []DeployJob{}
	}
	return out, nil
}

// GetDeployLogs returns the build log text for a job (may be empty early on).
func GetDeployLogs(baseURL, token, id string) (string, error) {
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/deploys/"+id+"/logs", nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return "", nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(raw), nil
}

func Deploy(baseURL, name, configTOML string, srcZip []byte) (*DeployResult, error) {
	return DeployWithAuth(baseURL, "", name, configTOML, srcZip)
}

func DeployWithAuth(baseURL, token, name, configTOML string, srcZip []byte) (*DeployResult, error) {
	body, _ := json.Marshal(map[string]string{
		"name": name, "config_toml": configTOML,
		"src_zip_b64": base64.StdEncoding.EncodeToString(srcZip),
	})
	req, err := http.NewRequest("POST", strings.TrimSuffix(baseURL, "/")+"/deploy", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("deploy: unauthorized (run `oort login`)")
	}
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
	return InvokeWithAuth(baseURL, "", "", fnPath, method, body)
}

func InvokeWithAuth(baseURL, token, apiKey, fnPath, method string, body []byte) (*InvokeResult, error) {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
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
	OwnerID       string         `json:"OwnerID"`
	Slug          string         `json:"Slug"`
	AuthMode      string         `json:"AuthMode"`
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
	return ListFunctionsWithAuth(baseURL, "")
}

func ListFunctionsWithAuth(baseURL, token string) ([]Function, error) {
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/functions", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized (run `oort login`)")
	}
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
	return GetLogsWithAuth(baseURL, "", fn)
}

func GetLogsWithAuth(baseURL, token, fn string) ([]LogLine, error) {
	req, err := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/logs?fn="+fn, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized (run `oort login`)")
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return nil, fmt.Errorf("logs: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out []LogLine
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- device login + operator + api keys ---

type DeviceStart struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	VerifyPath string `json:"verify_path"`
	ExpiresIn  int    `json:"expires_in"`
}

func DeviceStartReq(baseURL, deviceName string) (*DeviceStart, error) {
	body, _ := json.Marshal(map[string]string{"device_name": deviceName})
	resp, err := httpClient.Post(strings.TrimSuffix(baseURL, "/")+"/auth/device/start", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("login start: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out DeviceStart
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type DevicePoll struct {
	Status   string `json:"status"`
	Token    string `json:"token"`
	Username string `json:"username"`
}

func DevicePollReq(baseURL, deviceCode string) (*DevicePoll, error) {
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	resp, err := httpClient.Post(strings.TrimSuffix(baseURL, "/")+"/auth/device/poll", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out DevicePoll
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Whoami struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Setup     bool   `json:"setup"`
}

func Me(baseURL, token string) (*Whoami, error) {
	req, _ := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/auth/me", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized (run `oort login`)")
	}
	var out Whoami
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func LogoutRemote(baseURL, token string) {
	if token == "" {
		return
	}
	req, _ := http.NewRequest("POST", strings.TrimSuffix(baseURL, "/")+"/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

type APIKeyCreated struct {
	ID        string `json:"id"`
	Fn        string `json:"fn"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
}

func CreateAPIKey(baseURL, token, fn, name string) (*APIKeyCreated, error) {
	body, _ := json.Marshal(map[string]string{"fn": fn, "name": name})
	req, _ := http.NewRequest("POST", strings.TrimSuffix(baseURL, "/")+"/keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized (run `oort login`)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("keys create: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out APIKeyCreated
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type APIKeyInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  string `json:"created_at"`
	RevokedAt  string `json:"revoked_at"`
	LastUsedAt string `json:"last_used_at"`
}

func ListAPIKeys(baseURL, token, fn string) ([]APIKeyInfo, error) {
	req, _ := http.NewRequest("GET", strings.TrimSuffix(baseURL, "/")+"/keys?fn="+fn, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized (run `oort login`)")
	}
	var out []APIKeyInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []APIKeyInfo{}
	}
	return out, nil
}

func RevokeAPIKey(baseURL, token, id string) error {
	req, _ := http.NewRequest("POST", strings.TrimSuffix(baseURL, "/")+"/keys/"+id+"/revoke", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return fmt.Errorf("unauthorized (run `oort login`)")
	}
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		return fmt.Errorf("keys revoke: %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
