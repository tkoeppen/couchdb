package couchdb

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/flimzy/diff"
	"github.com/flimzy/kivik"
	"github.com/flimzy/kivik/driver"
	"github.com/flimzy/kivik/errors"
	"github.com/flimzy/testy"
)

func TestBulkDocs(t *testing.T) {
	tests := []struct {
		name    string
		db      *db
		docs    []interface{}
		options map[string]interface{}
		status  int
		err     string
	}{
		{
			name:   "network error",
			db:     newTestDB(nil, errors.New("net error")),
			status: 500,
			err:    "Post http://example.com/testdb/_bulk_docs: net error",
		},
		{
			name: "JSON encoding error",
			db: newTestDB(&http.Response{
				StatusCode: kivik.StatusOK,
				Body:       ioutil.NopCloser(strings.NewReader("")),
			}, nil),
			docs:   []interface{}{make(chan int)},
			status: kivik.StatusBadRequest,
			err:    "json: unsupported type: chan int",
		},
		{
			name: "docs rejected",
			db: newTestDB(&http.Response{
				StatusCode: kivik.StatusExpectationFailed,
				Body:       ioutil.NopCloser(strings.NewReader("[]")),
			}, nil),
			docs:   []interface{}{1, 2, 3},
			status: kivik.StatusExpectationFailed,
			err:    "Expectation Failed: one or more document was rejected",
		},
		{
			name: "bad request",
			db: newTestDB(&http.Response{
				StatusCode: kivik.StatusBadRequest,
				Body:       ioutil.NopCloser(strings.NewReader("")),
			}, nil),
			docs:   []interface{}{1, 2, 3},
			status: kivik.StatusBadRequest,
			err:    "Bad Request",
		},
		{
			name: "invalid JSON response",
			db: newTestDB(&http.Response{
				StatusCode: kivik.StatusCreated,
				Body:       ioutil.NopCloser(strings.NewReader("invalid json")),
			}, nil),
			docs:   []interface{}{1, 2, 3},
			status: 500,
			err:    "no closing delimiter: invalid character 'i' looking for beginning of value",
		},
		{
			name: "unexpected response code",
			db: newTestDB(&http.Response{
				StatusCode: kivik.StatusOK,
				Body:       ioutil.NopCloser(strings.NewReader("[]")),
			}, nil),
			docs: []interface{}{1, 2, 3},
		},
		{
			name:    "new_edits",
			options: map[string]interface{}{"new_edits": true},
			db: newCustomDB(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close() // nolint: errcheck
				var body struct {
					NewEdits bool `json:"new_edits"`
				}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					return nil, err
				}
				if !body.NewEdits {
					return nil, errors.New("`new_edits` not set")
				}
				return &http.Response{
					StatusCode: kivik.StatusCreated,
					Body:       ioutil.NopCloser(strings.NewReader("[]")),
				}, nil
			}),
		},
		{
			name:    "force_commit",
			options: map[string]interface{}{"force_commit": true},
			db: newCustomDB(func(req *http.Request) (*http.Response, error) {
				defer req.Body.Close() // nolint: errcheck
				var body map[string]interface{}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					return nil, err
				}
				if _, ok := body["force_commit"]; ok {
					return nil, errors.New("force_commit key found in body")
				}
				if value := req.Header.Get("X-Couch-Full-Commit"); value != "true" {
					return nil, errors.New("X-Couch-Full-Commit not set to true")
				}
				return &http.Response{
					StatusCode: kivik.StatusCreated,
					Body:       ioutil.NopCloser(strings.NewReader("[]")),
				}, nil
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.db.BulkDocs(context.Background(), test.docs, test.options)
			testy.StatusError(t, test.err, test.status, err)
		})
	}
}

func TestBulkNext(t *testing.T) {
	tests := []struct {
		name     string
		results  *bulkResults
		status   int
		err      string
		expected *driver.BulkResult
	}{
		{
			name: "no results",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[]`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			status: 500,
			err:    "EOF",
		},
		{
			name: "closing delimiter missing",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			status: 500,
			err:    "no closing delimiter: EOF",
		},
		{
			name: "invalid doc json",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[{foo}]`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			status: 500,
			err:    "invalid character 'f' looking for beginning of object key string",
		},
		{
			name: "successful update",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[{"id":"foo","rev":"1-xxx"}]`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			expected: &driver.BulkResult{
				ID:  "foo",
				Rev: "1-xxx",
			},
		},
		{
			name: "conflict",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[{"id":"foo","rev":"1-xxx","error":"conflict","reason":"annoying conflict"}]`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			expected: &driver.BulkResult{
				ID:    "foo",
				Rev:   "1-xxx",
				Error: errors.Status(kivik.StatusConflict, "annoying conflict"),
			},
		},
		{
			name: "conflict",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[{"id":"foo","error":"conflict","reason":"annoying conflict"}]`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			expected: &driver.BulkResult{
				ID:    "foo",
				Error: errors.Status(kivik.StatusConflict, "annoying conflict"),
			},
		},
		{
			name: "unknown error",
			results: func() *bulkResults {
				r, err := newBulkResults(Body(`[{"id":"foo","error":"foo","reason":"foo is erroneous"}]`))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}(),
			expected: &driver.BulkResult{
				ID:    "foo",
				Error: errors.Status(600, "foo is erroneous"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := new(driver.BulkResult)
			err := test.results.Next(result)
			testy.StatusError(t, test.err, test.status, err)
			if d := diff.Interface(test.expected, result); d != nil {
				t.Error(d)
			}
		})
	}
}
