package fix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/AbiXnash/codist-git"
	"github.com/AbiXnash/codist-git/testrepo"
	model "github.com/AbiXnash/codist-model"
	"github.com/AbiXnash/codist-review/store"
)

// fakeModel answers every chat with reply and records the prompts.
type fakeModel struct {
	topic   string // the answer to "is the suggestion about this problem?"
	reply   string
	prompts []string
}

func (m *fakeModel) client(t *testing.T) *model.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.prompts = append(m.prompts, req.Messages[len(req.Messages)-1].Content)
		reply := m.reply
		if strings.Contains(req.Messages[0].Content, "You decide whether") {
			reply = m.topic
			if reply == "" {
				reply = `{"about":true,"reason":""}`
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": reply}, "done": true})
	}))
	t.Cleanup(srv.Close)
	return model.New(srv.URL, "fake", 4096)
}

const title = "Exception swallowed by an empty catch block"

// setup is the test repository with the swallowed exception back at HEAD, and the issue stored for that commit.
func setup(t *testing.T) (*testrepo.Repo, *Fixer, *fakeModel) {
	tr := testrepo.New(t)
	for _, kv := range [][2]string{{"user.name", "Dev"}, {"user.email", "dev@shop"}, {"commit.gpgsign", "false"}} {
		tr.Git("config", kv[0], kv[1])
	}
	tr.Commit("Swallow discount errors again", map[string]string{testrepo.ServicePath: testrepo.ServiceBug})
	st, err := store.Open(t.TempDir(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sha := tr.Head()
	if err := st.SetReview(store.Review{SHA: sha, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceFindings(sha, []store.Finding{{SHA: sha, Path: testrepo.ServicePath, Line: testrepo.BugLine, EndLine: testrepo.BugLine,
		Severity: "medium", Source: "rule", Rule: "empty-catch", Title: title, Detail: "The error is lost.", Snippet: "} catch (Exception e) {", Key: "k1"}}); err != nil {
		t.Fatal(err)
	}
	repo, _ := git.Open(tr.Dir)
	fm := &fakeModel{reply: `{"onTopic":true,"edits":[{"find":"} catch (Exception e) {\n        }","replace":"} catch (Exception e) {\n            throw new IllegalStateException(e);\n        }"}],"why":"The error was swallowed; now it is rethrown.","message":"Rethrow discount errors"}`}
	return tr, &Fixer{Repo: repo, Store: st, Model: fm.client(t), Notes: filepath.Join(t.TempDir(), "notes.json")}, fm
}

func TestProposeAndApply(t *testing.T) {
	tr, f, fm := setup(t)
	sha := tr.Head()
	p, err := f.Propose(context.Background(), sha, testrepo.ServicePath, testrepo.BugLine, title, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Patch, "+            throw new IllegalStateException(e);") || !strings.Contains(p.Patch, "--- a/"+testrepo.ServicePath) {
		t.Fatalf("patch:\n%s", p.Patch)
	}
	if !strings.HasPrefix(p.Message, "Rethrow discount errors") || p.Finding.Suggestion == "" || len(p.Paths) != 1 || p.Branch != "main" {
		t.Fatalf("proposal %+v", p)
	}
	if !strings.Contains(fm.prompts[0], "The error is lost.") || !strings.Contains(fm.prompts[0], "} catch (Exception e) {") {
		t.Fatalf("the model should see the problem and the code:\n%s", fm.prompts[0])
	}
	if got, _ := os.ReadFile(filepath.Join(tr.Dir, filepath.FromSlash(testrepo.ServicePath))); strings.Contains(string(got), "IllegalStateException") {
		t.Fatal("nothing may change before it is applied")
	}
	if _, err := f.Apply(p.ID, " "); err == nil {
		t.Fatal("a commit message is needed")
	}
	commit, err := f.Apply(p.ID, "Rethrow discount errors")
	if err != nil {
		t.Fatal(err)
	}
	if commit == sha || tr.Git("log", "-1", "--format=%s") != "Rethrow discount errors" || tr.Git("status", "--porcelain") != "" {
		t.Fatalf("commit %s, status %q", commit, tr.Git("status", "--porcelain"))
	}
	if _, err := f.Apply(p.ID, "again"); err == nil {
		t.Fatal("an applied fix is gone")
	}
}

func TestOnlyTheFixIsCommitted(t *testing.T) {
	tr, f, _ := setup(t)
	other := filepath.Join(tr.Dir, "order-service/pom.xml")
	os.WriteFile(other, []byte("<project>edited</project>\n"), 0o644)
	tr.Git("add", "order-service/pom.xml") // staged, but not part of the fix
	p, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Apply(p.ID, "Rethrow"); err != nil {
		t.Fatal(err)
	}
	if files := tr.Git("show", "--name-only", "--format=", "HEAD"); files != testrepo.ServicePath {
		t.Fatalf("committed %q", files)
	}
}

func TestRefusesWhenTheCodeMoved(t *testing.T) {
	tr, f, _ := setup(t)
	full := filepath.Join(tr.Dir, filepath.FromSlash(testrepo.ServicePath))
	orig, _ := os.ReadFile(full)
	os.WriteFile(full, append(orig, []byte("// edit\n")...), 0o644)
	if _, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, ""); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("a file with uncommitted changes is not fixed: %v", err)
	}
	os.WriteFile(full, orig, 0o644)
	p, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, "")
	if err != nil {
		t.Fatal(err)
	}
	tr.Commit("Something else", map[string]string{"order-service/pom.xml": "<project>x</project>\n"})
	if _, err := f.Apply(p.ID, "Fix"); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("the branch moved: %v", err)
	}
}

func TestGuidedFixAndRefusal(t *testing.T) {
	tr, f, fm := setup(t)
	if _, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, "log it and rethrow"); err != nil {
		t.Fatal(err)
	}
	if last := fm.prompts[len(fm.prompts)-1]; !strings.Contains(last, "Fix it this way") || !strings.Contains(last, "log it and rethrow") {
		t.Fatal("the model must be told to follow the suggestion")
	}
	_, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, "also change OrderController")
	var refused Refused
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "OrderController") {
		t.Fatalf("a suggestion naming another class is refused without the model: %v", err)
	}
	fm.topic = `{"about":false,"reason":"That asks for a new feature, not a fix for this catch block."}`
	if _, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, "add loyalty points"); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "new feature") {
		t.Fatalf("the model's no is a refusal: %v", err)
	}
}

func TestBadEditsAreNotApplied(t *testing.T) {
	tr, f, fm := setup(t)
	fm.reply = `{"onTopic":true,"edits":[{"find":"this text is not in the file","replace":"x"}],"why":"","message":""}`
	if _, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, ""); err == nil {
		t.Fatal("an edit that does not match the code is refused")
	}
	fm.reply = `{"onTopic":true,"edits":[],"why":"","message":""}`
	if _, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, ""); err == nil {
		t.Fatal("no edit is no fix")
	}
	if _, err := f.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, "No such issue", ""); err == nil {
		t.Fatal("only issues Codist found can be fixed")
	}
}

func TestRememberRejections(t *testing.T) {
	tr, f, fm := setup(t)
	if err := f.Remember(Note{Title: title, Path: testrepo.ServicePath, Text: "We log and rethrow, never wrap."}); err != nil {
		t.Fatal(err)
	}
	again := &Fixer{Repo: f.Repo, Store: f.Store, Model: f.Model, Notes: f.Notes} // a restart reads the file
	if _, err := again.Propose(context.Background(), tr.Head(), testrepo.ServicePath, testrepo.BugLine, title, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fm.prompts[len(fm.prompts)-1], "We log and rethrow, never wrap.") {
		t.Fatal("the next fix must see why the last one was rejected")
	}
	if f.Remember(Note{Text: " "}) == nil {
		t.Fatal("a note needs a reason")
	}
}

func TestRoutes(t *testing.T) {
	tr, f, fm := setup(t)
	mux := http.NewServeMux()
	f.Routes(mux.HandleFunc)
	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}
	w := post("/api/fix", `{"sha":"`+tr.Head()+`","path":"`+testrepo.ServicePath+`","line":30,"title":"`+title+`"}`)
	var p Proposal
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.ID == "" {
		t.Fatalf("propose: %d %s", w.Code, w.Body)
	}
	fm.topic = `{"about":false,"reason":"Not this issue."}`
	if w := post("/api/fix", `{"sha":"`+tr.Head()+`","path":"`+testrepo.ServicePath+`","line":30,"title":"`+title+`","guidance":"other"}`); !strings.Contains(w.Body.String(), `"blocked":true`) {
		t.Fatalf("refusal: %s", w.Body)
	}
	if w := post("/api/memory", `{"title":"x","path":"y","text":"because"}`); w.Code != 200 {
		t.Fatalf("memory: %d", w.Code)
	}
	if w := post("/api/fix/"+p.ID+"/apply", `{"message":"Rethrow"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "commit") {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	if w := post("/api/fix", `{}`); w.Code != 400 {
		t.Fatalf("bad request: %d", w.Code)
	}
}

func TestModuleOf(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "svc/src/main/java/a"), 0o755)
	os.WriteFile(filepath.Join(dir, "svc/pom.xml"), []byte("<project/>"), 0o644)
	if m := moduleOf(dir, "svc/src/main/java/a/A.java"); m != "svc" {
		t.Fatalf("module %q", m)
	}
	if m := moduleOf(dir, "other/A.java"); m != "" {
		t.Fatalf("no pom: %q", m)
	}
	if c := (&Fixer{}).Check(context.Background(), "missing"); c.Status != "unavailable" {
		t.Fatalf("check of a missing fix: %+v", c)
	}
}

func TestReindent(t *testing.T) {
	src := "class A {\n    void a() {\n        redis.delete(KEY);\n    }\n}\n"
	got := reindent(src, "redis.delete(KEY);", "if (ok) {\n    redis.delete(KEY);\n}")
	if got != "if (ok) {\n            redis.delete(KEY);\n        }" {
		t.Fatalf("relative lines are shifted to the code: %q", got)
	}
	right := "if (ok) {\n            redis.delete(KEY);\n        }"
	if reindent(src, "redis.delete(KEY);", right) != right {
		t.Fatal("lines already in the file's indentation stay")
	}
	if reindent(src, "        redis.delete(KEY);", "x\ny") != "x\ny" {
		t.Fatal("a match from the start of the line is left alone")
	}
}
