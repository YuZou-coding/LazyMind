package academic

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lazymind/core/academic/arxivsource"
	"lazymind/core/common/orm"
)

func TestReadUnversionedPaperPropagatesArxivFailureBeforeDownload(t *testing.T) {
	previous := http.DefaultTransport
	calls := 0
	http.DefaultTransport = importErrorTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Hostname() != testArxivHost || r.URL.Path != "/api/query" || r.URL.Query().Get("id_list") != "2404.06654" {
			t.Errorf("unexpected request before revision lookup succeeded: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	installTestArxivClient(t)

	source := paperSource{DatasetID: "papers", Paper: PaperRef{PaperID: "paper", ArxivID: "2404.06654"}}
	pages, kind, hash, version, err := readKnownPaper(t.Context(), "u", source, orm.AcademicPaperContent{})
	var lookupError *arxivsource.LookupError
	if !errors.As(err, &lookupError) || lookupError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("provider failure was not preserved: %v", err)
	}
	if calls != 1 || len(pages) != 0 || kind != "" || hash != "" || version != "" {
		t.Fatalf("failed lookup produced content or started a download: calls=%d pages=%v kind=%q hash=%q version=%q", calls, pages, kind, hash, version)
	}
}

func TestExistingWorkPreviewPinsRequestedArxivRevision(t *testing.T) {
	db := paperFixture(t)
	if err := db.Model(&orm.AcademicWork{}).Where("id=?", "a").Updates(map[string]any{
		"arxiv_id_base": "1706.03762", "metadata_provenance_json": marshal(map[string]string{"pdf_version": "v6"}),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&orm.AcademicWorkDocument{AcademicWorkID: "a", DatasetID: "papers", DocumentID: "a", SourceProvider: "arxiv", SourceVersion: "v7"}).Error; err != nil {
		t.Fatal(err)
	}
	queries := []string{}
	previous := http.DefaultTransport
	http.DefaultTransport = importErrorTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusNotFound, `{}`
		if r.URL.Hostname() == testArxivHost {
			if r.Method != http.MethodGet || r.URL.Path != "/api/query" {
				t.Errorf("unexpected metadata request: %s %s", r.Method, r.URL)
			}
			queries = append(queries, r.URL.Query().Get("id_list"))
			status = http.StatusOK
			body = `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>https://arxiv.org/abs/1706.03762v6</id><title>Version six title</title><summary>Version six abstract</summary><published>2017-01-01T00:00:00Z</published><author><name>A. Author</name></author></entry></feed>`
		} else if r.URL.Hostname() == "arxiv.org" {
			t.Errorf("preview attempted a PDF request: %s", r.URL)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	installTestArxivClient(t)

	w := httptest.NewRecorder()
	PreviewImport(w, paperRequest(`{"target_dataset_id":"papers","work_ids":["a"],"resolve_external":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Token string        `json:"preview_token"`
		Items []PreviewItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Items) != 1 {
		t.Fatalf("invalid preview: %s (%v)", w.Body.String(), err)
	}
	item := response.Items[0]
	if len(queries) != 1 || queries[0] != "1706.03762v6" || item.WorkID != "a" || item.Paper.SourceVersion != "v6" || item.Paper.Title != "Version six title" || item.Disposition != "downloadable" || len(item.Candidates) != 1 || item.Candidates[0].Version != "v6" {
		t.Fatalf("existing work preview lost requested revision: queries=%v item=%+v", queries, item)
	}
	receipt, err := loadImportPreview(t.Context(), response.Token, "u", "papers")
	if err != nil || receipt.Verified["a"].SourceVersion != "v6" || receipt.Verified["a"].PDFURL != "https://arxiv.org/pdf/1706.03762v6" {
		t.Fatalf("verified revision was not saved for import: receipt=%+v error=%v", receipt, err)
	}
	var saved orm.AcademicWork
	if err := db.Where("id=?", "a").Take(&saved).Error; err != nil || saved.CanonicalTitle != "a" {
		t.Fatalf("preview rewrote shared bibliographic metadata: %+v (%v)", saved, err)
	}
}
