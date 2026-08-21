package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedSPAIsLocalAndServesFallback(t *testing.T) {
	handler := Handler()
	for _, path := range []string{"/", "/style.css", "/app.js", "/assets/skill:project:fixture"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, rec.Code)
		}
		body, err := io.ReadAll(rec.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(body) == 0 {
			t.Fatalf("GET %s returned empty body", path)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Flatline") || !strings.Contains(body, "app.js") {
		t.Fatalf("index missing local app markers: %q", body)
	}
	if strings.Contains(body, "https://") || strings.Contains(body, "http://") {
		t.Fatalf("index contains external resource URL: %q", body)
	}
	appJSReq := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	appJSRec := httptest.NewRecorder()
	handler.ServeHTTP(appJSRec, appJSReq)
	appJS := appJSRec.Body.String()
	for _, marker := range []string{"没有记录到与该资产相关的任务", "判定规则：", "观测等级", "参与记录", "section(\"没有相关任务记录\"", "section(\"不可观测\""} {
		if !strings.Contains(appJS, marker) {
			t.Fatalf("app.js is missing clear UI marker %q", marker)
		}
	}
	for _, term := range []string{"\u590d\u6d3b", "\u6682\u65e0\u673a\u4f1a", "\u672a\u53d1\u73b0\u53ef\u6bd4\u4efb\u52a1", "\u6062\u590d\u76d1\u62a4"} {
		if strings.Contains(appJS, term) {
			t.Fatalf("app.js contains forbidden user-facing term %q", term)
		}
	}
}
