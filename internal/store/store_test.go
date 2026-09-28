package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaInSync(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != schemaSQL {
		t.Fatal("internal/store/schema.sql drifted from db/schema.sql; copy it over")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertFunction("hello", "", "name=\"hello\""); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertVersion(Version{
		Name: "hello", Ver: "0.1.0", SHA256: "abc",
		HandlerKey: "bundles/hello/0.1.0/handler", ConfigJSON: "{}", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertFunction("hello", "0.1.0", "name=\"hello\""); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFunction("hello")
	if err != nil {
		t.Fatal(err)
	}
	if f.ActiveVersion != "0.1.0" {
		t.Fatalf("active = %q, want 0.1.0", f.ActiveVersion)
	}
	if _, err := s.GetVersion("hello", "0.1.0"); err != nil {
		t.Fatalf("version lost: %v", err)
	}
	fns, err := s.ListFunctions()
	if err != nil || len(fns) != 1 {
		t.Fatalf("list = %+v, err = %v", fns, err)
	}
}

func TestListVersionsOrder(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, ver := range []string{"0.1.0", "0.2.0", "0.1.1"} {
		if err := s.InsertVersion(Version{
			Name: "hello", Ver: ver, SHA256: "abc",
			HandlerKey: "bundles/hello/" + ver + "/handler", ConfigJSON: "{}", Status: "active",
		}); err != nil {
			t.Fatal(err)
		}
	}
	vers, err := s.ListVersions("hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) != 3 {
		t.Fatalf("len = %d, want 3", len(vers))
	}
}

func TestActiveVersionCreatedAt(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := s.ActiveVersionCreatedAt("nope", "v1"); got != "" {
		t.Fatalf("unknown = %q, want empty", got)
	}
	if err := s.InsertVersion(Version{
		Name: "hello", Ver: "v1", SHA256: "abc",
		HandlerKey: "bundles/hello/v1/handler", ConfigJSON: "{}", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	got := s.ActiveVersionCreatedAt("hello", "v1")
	if got == "" {
		t.Fatal("expected non-empty created_at")
	}
	if len(got) < 10 || got[:4] < "2020" {
		t.Fatalf("created_at = %q, want UTC timestamp", got)
	}
}

func TestDeployJobs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	j1, err := s.CreateJob("owner1", "hello", "name=\"hello\"", "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if j1.Status != JobQueued || j1.Version != "0.1.0" {
		t.Fatalf("job = %+v", j1)
	}
	j2, err := s.CreateJob("owner1", "hello", "name=\"hello\"", "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	// Claim is FIFO.
	claimed, ok := s.ClaimNextJob()
	if !ok || claimed.ID != j1.ID || claimed.Status != JobBuilding {
		t.Fatalf("claimed = %+v, %v", claimed, ok)
	}
	if _, ok := s.ClaimNextJob(); !ok {
		t.Fatal("second claim should get j2")
	}
	if _, ok := s.ClaimNextJob(); ok {
		t.Fatal("empty queue should not claim")
	}
	// Finish j1, fail j2.
	if err := s.UpdateJobStatus(j1.ID, JobActive, "0.1.0", "img", "sha", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateJobStatus(j2.ID, JobFailed, "0.2.0", "", "", "build boom"); err != nil {
		t.Fatal(err)
	}
	gotJob, err := s.GetJob(j2.ID)
	if err != nil || gotJob.Status != JobFailed || gotJob.Error != "build boom" {
		t.Fatalf("got = %+v, err = %v", gotJob, err)
	}
	jobs, err := s.ListJobs("hello", 10)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("list = %+v, err = %v", jobs, err)
	}
	if jobs[0].ID != j2.ID {
		t.Fatal("list should be newest first")
	}
	// Crash recovery: building -> queued.
	j3, _ := s.CreateJob("owner1", "hello", "name=\"hello\"", "0.3.0")
	if _, ok := s.ClaimNextJob(); !ok {
		t.Fatal("claim j3")
	}
	if n := s.RequeueStuck(); n != 1 {
		t.Fatalf("requeued = %d, want 1", n)
	}
	re, _ := s.GetJob(j3.ID)
	if re.Status != JobQueued {
		t.Fatalf("requeued status = %q", re.Status)
	}
}
