package stats

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

type fixedChunkReader struct {
	data      []byte
	offset    int
	chunkSize int
}

func (r *fixedChunkReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := r.chunkSize
	if n > len(p) {
		n = len(p)
	}
	if rem := len(r.data) - r.offset; n > rem {
		n = rem
	}
	copy(p[:n], r.data[r.offset:r.offset+n])
	r.offset += n
	return n, nil
}

type errAfterReader struct {
	data   []byte
	offset int
	limit  int
	err    error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.offset >= r.limit {
		return 0, r.err
	}
	maxRead := r.limit - r.offset
	if maxRead > len(p) {
		maxRead = len(p)
	}
	if rem := len(r.data) - r.offset; maxRead > rem {
		maxRead = rem
	}
	if maxRead == 0 {
		return 0, r.err
	}
	copy(p[:maxRead], r.data[r.offset:r.offset+maxRead])
	r.offset += maxRead
	return maxRead, nil
}

func formatRawRecord(hash, authorName, body, numstat string) string {
	return commitStartMarker + hash + "\x00" + authorName + "\x00" + body + bodyEndMarker + numstat
}

func collectStreamRecords(r io.Reader) ([]commitRecord, error) {
	var records []commitRecord
	err := streamCommitRecords(r, func(rec commitRecord) {
		records = append(records, rec)
	})
	return records, err
}

func initStreamTestRepo(t *testing.T, repoDir string) {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", repoDir, err)
	}
	runStreamGit(t, repoDir, nil, "init")
	runStreamGit(t, repoDir, nil, "config", "user.name", "Shelby Miller")
	runStreamGit(t, repoDir, nil, "config", "user.email", "shelby@example.com")
	runStreamGit(t, repoDir, nil, "config", "commit.gpgsign", "false")
}

func runStreamGit(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	env = append(env, extraEnv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\noutput: %s", args, err, string(out))
	}
	return string(out)
}

func writeStreamFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func commitStreamChanges(t *testing.T, repoDir, msgDir, authorName, dateISO, message string, allowEmpty bool) {
	t.Helper()
	if !allowEmpty {
		runStreamGit(t, repoDir, nil, "add", "-A")
	}
	msgFile, err := os.CreateTemp(msgDir, "commit-msg-*.txt")
	if err != nil {
		t.Fatalf("create temp commit message file: %v", err)
	}
	msgPath := msgFile.Name()
	if _, err := msgFile.WriteString(message); err != nil {
		_ = msgFile.Close()
		t.Fatalf("write commit message file: %v", err)
	}
	if err := msgFile.Close(); err != nil {
		t.Fatalf("close commit message file: %v", err)
	}

	env := []string{
		"GIT_AUTHOR_DATE=" + dateISO,
		"GIT_COMMITTER_DATE=" + dateISO,
	}
	if authorName != "" {
		env = append(env, "GIT_AUTHOR_NAME="+authorName)
	}
	args := []string{"commit", "-F", msgPath}
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	runStreamGit(t, repoDir, env, args...)
}

func TestStreamCommitRecords_OneByteReaderAndChunkBoundaries(t *testing.T) {
	t.Parallel()

	aiBody := strings.Join([]string{
		"feat: stream git log parsing",
		"",
		"10\t20\tfake_numstat_in_body.go",
		"",
		"Assisted-by: pi:claude-sonnet-4 [go-test]",
		"X-Agent-Planner: openai/o3",
		"X-Agent-Implementer: anthropic/claude-sonnet-4",
		"X-Agent-Harness: pi@1.0.0",
		"",
	}, "\n")
	humanBody := "fix: handle CRLF numstat\r\n\r\nManual human commit.\r\n"

	var stream strings.Builder
	stream.WriteString(formatRawRecord(
		"1111111111111111111111111111111111111111",
		"Shelby Miller by claude-sonnet-4 & 1 more",
		aiBody,
		"\n12\t4\tinternal/stats/stats.go\r\n-\t-\tassets/logo.png\r\n8\t0\tREADME.md\r\n",
	))
	stream.WriteString(formatRawRecord(
		"2222222222222222222222222222222222222222",
		"Shelby Miller",
		"chore: empty commit\n",
		"",
	))
	stream.WriteString(formatRawRecord(
		"3333333333333333333333333333333333333333",
		"Alice Chen",
		humanBody,
		"\r\n3\t1\tcmd/git-by/main.go\r\n-\t-\tfixtures/sample.bin\n15\t7\tinternal/git/git.go\r\n",
	))
	stream.WriteString(formatRawRecord(
		"4444444444444444444444444444444444444444",
		"Bob by gpt-4.1",
		"assets: binary-only update\n",
		"\n-\t-\ticon.ico\n-\t-\tbanner.png\n",
	))
	stream.WriteString(formatRawRecord(
		"5555555555555555555555555555555555555555",
		"Human Dev",
		"",
		"\n100\t25\tfinal.go",
	))

	raw := []byte(stream.String())
	want := []commitRecord{
		{
			hash:       "1111111111111111111111111111111111111111",
			authorName: "Shelby Miller by claude-sonnet-4 & 1 more",
			body:       aiBody,
			additions:  20,
			deletions:  4,
		},
		{
			hash:       "2222222222222222222222222222222222222222",
			authorName: "Shelby Miller",
			body:       "chore: empty commit\n",
			additions:  0,
			deletions:  0,
		},
		{
			hash:       "3333333333333333333333333333333333333333",
			authorName: "Alice Chen",
			body:       humanBody,
			additions:  18,
			deletions:  8,
		},
		{
			hash:       "4444444444444444444444444444444444444444",
			authorName: "Bob by gpt-4.1",
			body:       "assets: binary-only update\n",
			additions:  0,
			deletions:  0,
		},
		{
			hash:       "5555555555555555555555555555555555555555",
			authorName: "Human Dev",
			body:       "",
			additions:  100,
			deletions:  25,
		},
	}
	wantIsAI := []bool{true, false, false, true, false}

	readers := []struct {
		name string
		make func([]byte) io.Reader
	}{
		{
			name: "full_bytes_reader",
			make: func(b []byte) io.Reader { return bytes.NewReader(b) },
		},
		{
			name: "iotest_one_byte_reader",
			make: func(b []byte) io.Reader { return iotest.OneByteReader(bytes.NewReader(b)) },
		},
		{
			name: "iotest_data_err_one_byte_reader",
			make: func(b []byte) io.Reader { return iotest.DataErrReader(iotest.OneByteReader(bytes.NewReader(b))) },
		},
		{
			name: "chunk_7_bytes",
			make: func(b []byte) io.Reader { return &fixedChunkReader{data: b, chunkSize: 7} },
		},
		{
			name: "chunk_13_bytes",
			make: func(b []byte) io.Reader { return &fixedChunkReader{data: b, chunkSize: 13} },
		},
	}

	for _, rc := range readers {
		t.Run(rc.name, func(t *testing.T) {
			t.Parallel()
			got, err := collectStreamRecords(rc.make(raw))
			if err != nil {
				t.Fatalf("streamCommitRecords returned unexpected error: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("len(records) = %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("record[%d] = %#v, want %#v", i, got[i], want[i])
				}
				prov := parseProvenance(got[i].authorName, got[i].body)
				if prov.isAI != wantIsAI[i] {
					t.Errorf("record[%d] parseProvenance().isAI = %v, want %v", i, prov.isAI, wantIsAI[i])
				}
			}
		})
	}
}

func TestStreamCommitRecords_LargeBodyExceedingBufferAndManyNumstatLines(t *testing.T) {
	t.Parallel()

	// Commit 1: >256KB body (exceeds both 64KB streamBufSize and 256KB maxRetainedBodyCap)
	// sized so the leading NUL of bodyEndMarker lands on the exact last byte (index 65535)
	// of bufio.Reader's 64KB internal buffer, splitting bodyEndMarker across two fills.
	hash1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	author1 := "Shelby Miller by claude-sonnet-4"
	trailer1 := "\n\nAssisted-by: pi:claude-sonnet-4\nX-Agent-Implementer: anthropic/claude-sonnet-4\n"
	targetBody1Len := 4*streamBufSize - 1 // 262,143 bytes (>200KB and >maxRetainedBodyCap)
	filler1Len := targetBody1Len - len(trailer1)
	body1 := strings.Repeat("A", filler1Len) + trailer1
	if len(body1) != targetBody1Len {
		t.Fatalf("body1 len = %d, want %d", len(body1), targetBody1Len)
	}

	// Build 5,000 numstat lines (plus interspersed binary entries).
	const numstatLines = 5000
	var numstat1 strings.Builder
	numstat1.WriteByte('\n')
	wantAdd1 := 0
	wantDel1 := 0
	for i := 1; i <= numstatLines; i++ {
		if i%250 == 0 {
			fmt.Fprintf(&numstat1, "-\t-\tassets/bin_%04d.dat\n", i)
			continue
		}
		a := (i % 19) + 1
		d := i % 11
		wantAdd1 += a
		wantDel1 += d
		fmt.Fprintf(&numstat1, "%d\t%d\tpkg/mod_%02d/file_%04d.go\n", a, d, i%20, i)
	}

	// Commit 2: >200KB body where bodyEndMarker straddles a 64KB stream offset boundary
	// mid-marker (first 7 bytes before the 64KB boundary, remaining 9 bytes after).
	hash2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	author2 := "Alice Chen"
	prefix1 := formatRawRecord(hash1, author1, body1, numstat1.String())
	commit2HeaderPrefix := commitStartMarker + hash2 + "\x00" + author2 + "\x00"
	baseOffset := len(prefix1) + len(commit2HeaderPrefix)
	minBody2Len := 210 * 1024 // >200KB
	nextBoundary := ((baseOffset+minBody2Len)/streamBufSize + 1) * streamBufSize
	targetBody2Len := nextBoundary - baseOffset - 7
	body2Prefix := "feat: straddle 64KB stream boundary\n\n"
	body2Suffix := "\nend-of-large-body-2\n"
	body2 := body2Prefix + strings.Repeat("B", targetBody2Len-len(body2Prefix)-len(body2Suffix)) + body2Suffix
	if markerStart := baseOffset + len(body2); (markerStart % streamBufSize) != streamBufSize-7 {
		t.Fatalf("bodyEndMarker start offset %d mod %d = %d, want %d",
			markerStart, streamBufSize, markerStart%streamBufSize, streamBufSize-7)
	}
	numstat2 := "\n42\t17\tinternal/stats/stats.go\n"

	// Commit 3: small follow-up commit to verify clean buffer reuse after maxRetainedBodyCap release.
	hash3 := "cccccccccccccccccccccccccccccccccccccccc"
	author3 := "Shelby Miller"
	body3 := "chore: post-large commit\n"
	numstat3 := "\n5\t2\tREADME.md\n"

	raw := []byte(prefix1 +
		formatRawRecord(hash2, author2, body2, numstat2) +
		formatRawRecord(hash3, author3, body3, numstat3))

	want := []commitRecord{
		{
			hash:       hash1,
			authorName: author1,
			body:       body1,
			additions:  wantAdd1,
			deletions:  wantDel1,
		},
		{
			hash:       hash2,
			authorName: author2,
			body:       body2,
			additions:  42,
			deletions:  17,
		},
		{
			hash:       hash3,
			authorName: author3,
			body:       body3,
			additions:  5,
			deletions:  2,
		},
	}

	readerCases := []struct {
		name string
		make func([]byte) io.Reader
	}{
		{
			name: "full_reader",
			make: func(b []byte) io.Reader { return bytes.NewReader(b) },
		},
		{
			name: "chunk_64kb_boundary",
			make: func(b []byte) io.Reader { return &fixedChunkReader{data: b, chunkSize: streamBufSize} },
		},
		{
			name: "chunk_4099_prime",
			make: func(b []byte) io.Reader { return &fixedChunkReader{data: b, chunkSize: 4099} },
		},
	}

	for _, rc := range readerCases {
		t.Run(rc.name, func(t *testing.T) {
			t.Parallel()
			got, err := collectStreamRecords(rc.make(raw))
			if err != nil {
				t.Fatalf("streamCommitRecords returned unexpected error: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("len(records) = %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].hash != want[i].hash ||
					got[i].authorName != want[i].authorName ||
					got[i].additions != want[i].additions ||
					got[i].deletions != want[i].deletions {
					t.Errorf("record[%d] metadata/numstat mismatch: got (hash=%q author=%q +%d/-%d), want (hash=%q author=%q +%d/-%d)",
						i, got[i].hash, got[i].authorName, got[i].additions, got[i].deletions,
						want[i].hash, want[i].authorName, want[i].additions, want[i].deletions)
				}
				if got[i].body != want[i].body {
					t.Errorf("record[%d] body mismatch: len(got)=%d, len(want)=%d", i, len(got[i].body), len(want[i].body))
				}
			}
		})
	}
}

func TestStreamCommitRecords_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("empty_stream", func(t *testing.T) {
		t.Parallel()
		got, err := collectStreamRecords(strings.NewReader(""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d records, want 0", len(got))
		}
	})

	t.Run("leading_garbage_only", func(t *testing.T) {
		t.Parallel()
		garbage := "random preamble\x00GITBY_COMMIT_ST\x00more garbage\n1\t2\tignored.go\n"
		got, err := collectStreamRecords(iotest.OneByteReader(strings.NewReader(garbage)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %d records, want 0", len(got))
		}
	})

	t.Run("leading_garbage_before_valid_record", func(t *testing.T) {
		t.Parallel()
		raw := "warning: noise\x00\x00GITBY_COMMIT_STAR" + formatRawRecord(
			"1234567890abcdef",
			"Shelby Miller",
			"feat: after garbage\n",
			"\n2\t1\ta.go\n",
		)
		got, err := collectStreamRecords(iotest.OneByteReader(strings.NewReader(raw)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		if got[0].hash != "1234567890abcdef" || got[0].additions != 2 || got[0].deletions != 1 {
			t.Errorf("unexpected record: %#v", got[0])
		}
	})

	t.Run("truncated_final_record_missing_body_end_marker", func(t *testing.T) {
		t.Parallel()
		raw := formatRawRecord("aaaa", "Alice", "first commit\n", "\n3\t1\ta.go\n") +
			commitStartMarker + "bbbb\x00Bob\x00truncated body without full end marker\x00GITBY_BODY_EN"
		got, err := collectStreamRecords(iotest.OneByteReader(strings.NewReader(raw)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		if got[0].hash != "aaaa" || got[0].additions != 3 || got[0].deletions != 1 {
			t.Errorf("unexpected record[0]: %#v", got[0])
		}
	})

	t.Run("malformed_header_missing_nul_is_skipped", func(t *testing.T) {
		t.Parallel()
		raw := commitStartMarker + "malformed-without-nul" + bodyEndMarker + "\n99\t99\tignored.go\n" +
			formatRawRecord("cccc", "Carol", "valid follow-up\n", "\n4\t2\tok.go\n")
		got, err := collectStreamRecords(iotest.OneByteReader(strings.NewReader(raw)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		if got[0].hash != "cccc" || got[0].authorName != "Carol" || got[0].additions != 4 || got[0].deletions != 2 {
			t.Errorf("unexpected record: %#v", got[0])
		}
	})

	t.Run("embedded_single_nul_and_double_nul_before_body_end", func(t *testing.T) {
		t.Parallel()
		// Trailing "\x00" in body1 creates "\x00\x00GITBY_BODY_END\x00" on the wire.
		body1 := "subject\n\nembedded single\x00nul and partial\x00GITBY_BODY_prefix and trailing nul\x00"
		body2 := "second\x00\x00double nul inside body\n"
		raw := []byte(
			formatRawRecord("d1", "Dev One", body1, "\n6\t1\tone.go\n") +
				formatRawRecord("d2", "Dev Two", body2, "\n2\t3\ttwo.go\n"),
		)

		for _, tc := range []struct {
			name string
			r    io.Reader
		}{
			{"full", bytes.NewReader(raw)},
			{"one_byte", iotest.OneByteReader(bytes.NewReader(raw))},
		} {
			got, err := collectStreamRecords(tc.r)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
			if len(got) != 2 {
				t.Fatalf("%s: len(got) = %d, want 2", tc.name, len(got))
			}
			if got[0].body != body1 || got[0].additions != 6 || got[0].deletions != 1 {
				t.Errorf("%s: record[0] mismatch: got %#v, want body=%q +6/-1", tc.name, got[0], body1)
			}
			if got[1].body != body2 || got[1].additions != 2 || got[1].deletions != 3 {
				t.Errorf("%s: record[1] mismatch: got %#v, want body=%q +2/-3", tc.name, got[1], body2)
			}
		}
	})

	t.Run("numstat_last_line_without_newline_directly_before_next_commit_marker", func(t *testing.T) {
		t.Parallel()
		raw := []byte(
			formatRawRecord("e1", "Author 1", "commit 1\n", "\n10\t3\tfirst.go\n7\t2\tno_trailing_newline.go") +
				formatRawRecord("e2", "Author 2", "commit 2\n", "\n4\t1\twith_nul_before_marker.go\x00") +
				formatRawRecord("e3", "Author 3", "commit 3\n", "\n9\t5\teof_without_newline.go"),
		)

		for _, tc := range []struct {
			name string
			r    io.Reader
		}{
			{"full", bytes.NewReader(raw)},
			{"one_byte", iotest.OneByteReader(bytes.NewReader(raw))},
			{"chunk_7", &fixedChunkReader{data: raw, chunkSize: 7}},
		} {
			got, err := collectStreamRecords(tc.r)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
			if len(got) != 3 {
				t.Fatalf("%s: len(got) = %d, want 3", tc.name, len(got))
			}
			if got[0].additions != 17 || got[0].deletions != 5 {
				t.Errorf("%s: record[0] = +%d/-%d, want +17/-5", tc.name, got[0].additions, got[0].deletions)
			}
			if got[1].additions != 4 || got[1].deletions != 1 {
				t.Errorf("%s: record[1] = +%d/-%d, want +4/-1", tc.name, got[1].additions, got[1].deletions)
			}
			if got[2].additions != 9 || got[2].deletions != 5 {
				t.Errorf("%s: record[2] = +%d/-%d, want +9/-5", tc.name, got[2].additions, got[2].deletions)
			}
		}
	})

	t.Run("numstat_cjk_spaces_rename_and_binary_paths", func(t *testing.T) {
		t.Parallel()
		numstat := strings.Join([]string{
			"",
			"11\t2\t文档/中文路径/设计说明.md",
			"5\t1\tpath with spaces/my file (v2).go",
			"8\t3\tinternal/{old_dir => new_dir}/stream.go",
			"2\t4\t{old name.txt => dir with space/new name.txt}",
			"-\t-\tbin.png",
			"-\t-\tassets/图片 样本.bin\r",
			"   \r",
			"not-a-valid-numstat-line",
			"abc\t1\tinvalid_add.go",
			"1\txyz\tinvalid_del.go",
			"",
		}, "\n")
		raw := []byte(formatRawRecord("f1", "Dev", "feat: special paths\n", numstat))

		for _, tc := range []struct {
			name string
			r    io.Reader
		}{
			{"full", bytes.NewReader(raw)},
			{"one_byte", iotest.OneByteReader(bytes.NewReader(raw))},
		} {
			got, err := collectStreamRecords(tc.r)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", tc.name, err)
			}
			if len(got) != 1 {
				t.Fatalf("%s: len(got) = %d, want 1", tc.name, len(got))
			}
			// 11+5+8+2 = 26 additions, 2+1+3+4 = 10 deletions.
			if got[0].additions != 26 || got[0].deletions != 10 {
				t.Errorf("%s: got +%d/-%d, want +26/-10", tc.name, got[0].additions, got[0].deletions)
			}
		}
	})

	t.Run("mid_stream_io_error_propagation", func(t *testing.T) {
		t.Parallel()
		errInjected := errors.New("injected stream read failure")
		rec1 := formatRawRecord("1111", "Alice", "commit one body text\n", "\n10\t2\ta.go\n3\t1\tb.go\n")
		rec2 := formatRawRecord("2222", "Bob", "commit two body text\n", "\n1\t0\tc.go\n")
		full := []byte(rec1 + rec2)

		bodyEndIdx := strings.Index(rec1, bodyEndMarker)
		numstatStartIdx := bodyEndIdx + len(bodyEndMarker)
		secondCommitStartIdx := len(rec1)

		cutPoints := []struct {
			name  string
			limit int
		}{
			{"before_first_commit_marker", 5},
			{"inside_first_commit_body", len(commitStartMarker) + 15},
			{"mid_body_end_marker_peek", bodyEndIdx + 4},
			{"mid_numstat_section", numstatStartIdx + 6},
			{"mid_next_commit_marker_peek", secondCommitStartIdx + 5},
		}

		for _, cp := range cutPoints {
			t.Run(cp.name, func(t *testing.T) {
				t.Parallel()
				r := &errAfterReader{
					data:  full,
					limit: cp.limit,
					err:   errInjected,
				}
				_, err := collectStreamRecords(r)
				if !errors.Is(err, errInjected) {
					t.Fatalf("error = %v, want %v", err, errInjected)
				}
			})
		}
	})
}

func TestCollect_StreamingLargeHistoryConsistency(t *testing.T) {
	t.Parallel()

	t.Run("empty_repo_returns_zero_report", func(t *testing.T) {
		t.Parallel()
		emptyRepo := filepath.Join(t.TempDir(), "stream-empty-repo")
		initStreamTestRepo(t, emptyRepo)

		rep, err := Collect(emptyRepo, Options{})
		if err != nil {
			t.Fatalf("Collect on empty repo returned error: %v", err)
		}
		if rep.RepoName != "stream-empty-repo" {
			t.Errorf("RepoName = %q, want %q", rep.RepoName, "stream-empty-repo")
		}
		if rep.TotalCommits != 0 || rep.TotalLines != 0 {
			t.Errorf("TotalCommits=%d TotalLines=%d, want 0 and 0", rep.TotalCommits, rep.TotalLines)
		}
		if rep.Human != (Breakdown{}) || rep.AI != (Breakdown{}) {
			t.Errorf("expected zero breakdowns, got human=%+v ai=%+v", rep.Human, rep.AI)
		}
		if rep.Implementers == nil || len(rep.Implementers) != 0 {
			t.Errorf("Implementers = %#v, want empty non-nil slice", rep.Implementers)
		}
		if rep.Planners == nil || len(rep.Planners) != 0 {
			t.Errorf("Planners = %#v, want empty non-nil slice", rep.Planners)
		}
	})

	t.Run("mixed_history_with_empty_binary_large_ai_and_human_commits", func(t *testing.T) {
		t.Parallel()
		tmpRoot := t.TempDir()
		repoDir := filepath.Join(tmpRoot, "stream-history-repo")
		msgDir := filepath.Join(tmpRoot, "msgs")
		if err := os.MkdirAll(msgDir, 0o755); err != nil {
			t.Fatalf("mkdir msgDir: %v", err)
		}
		initStreamTestRepo(t, repoDir)

		// 1. Pure human commit (+7 / -0).
		writeStreamFile(t, filepath.Join(repoDir, "human_base.txt"), []byte("h1\nh2\nh3\nh4\nh5\n"))
		writeStreamFile(t, filepath.Join(repoDir, "obsolete.txt"), []byte("drop1\ndrop2\n"))
		commitStreamChanges(t, repoDir, msgDir, "Shelby Miller", "2026-01-01T10:00:00Z", "feat: initial human scaffold", false)

		// 2. Empty human commit (+0 / -0).
		commitStreamChanges(t, repoDir, msgDir, "Shelby Miller", "2026-01-02T10:00:00Z", "chore: empty human checkpoint", true)

		// 3. Binary-only AI commit (+0 / -0).
		writeStreamFile(t, filepath.Join(repoDir, "assets", "icon.bin"), []byte{0x00, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff})
		commitStreamChanges(t, repoDir, msgDir, "Shelby Miller by claude-sonnet-4", "2026-01-03T10:00:00Z",
			"assets: add binary icon\n\nX-Agent-Implementer: anthropic/claude-sonnet-4\nX-Agent-Harness: pi@1.0.0", false)

		// 4. Multi-role AI commit with >70KB commit body (exceeds 64KB stream buffer) + text edit (+4 / -1) + binary file.
		writeStreamFile(t, filepath.Join(repoDir, "human_base.txt"), []byte("h1\nh2_ai\nh3\nh4\nh5\nh6\nh7\nh8\n"))
		writeStreamFile(t, filepath.Join(repoDir, "assets", "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00, 0xfe, 0xff})
		largeBody := strings.Join([]string{
			"feat: multi-role relay with oversized commit body",
			"",
			strings.Repeat("Detailed architecture rationale line for streaming parser verification.\n", 1100),
			"Assisted-by: pi:claude-sonnet-4 [go-test]",
			"X-Agent-Orchestrator: anthropic/claude-opus-4",
			"X-Agent-Planner: openai/o3",
			"X-Agent-Implementer: anthropic/claude-sonnet-4, google/gemini-2.5-pro",
			"X-Agent-Reviewer: openai/gpt-4.1",
			"X-Agent-Committer: google/gemini-2.5-flash",
			"X-Agent-Harness: pi@1.0.0",
		}, "\n")
		commitStreamChanges(t, repoDir, msgDir, "Shelby Miller by claude-sonnet-4 & 1 more", "2026-01-04T10:00:00Z", largeBody, false)

		// 5. Empty AI commit (+0 / -0) with Planner, Reviewer, Harness.
		commitStreamChanges(t, repoDir, msgDir, "Shelby Miller by o3", "2026-01-05T10:00:00Z",
			"chore: empty AI planning checkpoint\n\nX-Agent-Planner: openai/o3\nX-Agent-Reviewer: openai/gpt-4.1\nX-Agent-Harness: pi@1.0.0", true)

		// 6..15. 10 pure human commits (commit 6 removes obsolete.txt -2 and adds 3 lines; commits 7..15 each add 3 lines).
		if err := os.Remove(filepath.Join(repoDir, "obsolete.txt")); err != nil {
			t.Fatalf("remove obsolete.txt: %v", err)
		}
		for i := 1; i <= 10; i++ {
			writeStreamFile(t, filepath.Join(repoDir, "human", fmt.Sprintf("h_%02d.txt", i)),
				[]byte(fmt.Sprintf("line1-%d\nline2-%d\nline3-%d\n", i, i, i)))
			commitStreamChanges(t, repoDir, msgDir, "Shelby Miller",
				fmt.Sprintf("2026-01-%02dT10:00:00Z", 5+i),
				fmt.Sprintf("docs: human change %d", i), false)
		}

		// 16..20. 5 AI commits with explicit Implementer + Planner + Harness (+4 / -0 each).
		for i := 1; i <= 5; i++ {
			writeStreamFile(t, filepath.Join(repoDir, "ai", fmt.Sprintf("a_%02d.txt", i)),
				[]byte(fmt.Sprintf("a1-%d\na2-%d\na3-%d\na4-%d\n", i, i, i, i)))
			commitStreamChanges(t, repoDir, msgDir, "Shelby Miller by claude-sonnet-4",
				fmt.Sprintf("2026-01-%02dT10:00:00Z", 15+i),
				fmt.Sprintf("feat: ai implementer batch %d\n\nX-Agent-Implementer: anthropic/claude-sonnet-4\nX-Agent-Planner: openai/o3\nX-Agent-Harness: pi@1.0.0", i), false)
		}

		// 21..25. 5 AI commits with Kernel-style Assisted-by only (+4 / -0 each).
		for i := 6; i <= 10; i++ {
			writeStreamFile(t, filepath.Join(repoDir, "ai", fmt.Sprintf("a_%02d.txt", i)),
				[]byte(fmt.Sprintf("b1-%d\nb2-%d\nb3-%d\nb4-%d\n", i, i, i, i)))
			commitStreamChanges(t, repoDir, msgDir, "Shelby Miller",
				fmt.Sprintf("2026-01-%02dT10:00:00Z", 15+i),
				fmt.Sprintf("fix: assisted batch %d\n\nAssisted-by: cursor:google/gemini-2.5-pro [go-test]", i), false)
		}

		rep, err := Collect(repoDir, Options{TopN: 10})
		if err != nil {
			t.Fatalf("Collect failed: %v", err)
		}

		// Totals: 25 commits (12 Human, 13 AI); 84 lines (Human +37/-2 = 39, AI +44/-1 = 45).
		if rep.RepoName != "stream-history-repo" {
			t.Errorf("RepoName = %q, want %q", rep.RepoName, "stream-history-repo")
		}
		if rep.TotalCommits != 25 || rep.TotalLines != 84 {
			t.Fatalf("TotalCommits=%d TotalLines=%d, want 25 and 84", rep.TotalCommits, rep.TotalLines)
		}
		wantHuman := Breakdown{
			Commits:       12,
			CommitPercent: 48,
			Additions:     37,
			Deletions:     2,
			LinesPercent:  46.43,
		}
		wantAI := Breakdown{
			Commits:       13,
			CommitPercent: 52,
			Additions:     44,
			Deletions:     1,
			LinesPercent:  53.57,
		}
		if rep.Human != wantHuman {
			t.Errorf("Human = %+v, want %+v", rep.Human, wantHuman)
		}
		if rep.AI != wantAI {
			t.Errorf("AI = %+v, want %+v", rep.AI, wantAI)
		}

		assertLeaderboard := func(label string, got, want []ModelCount) {
			t.Helper()
			if len(got) != len(want) {
				t.Fatalf("%s = %+v, want %+v", label, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s[%d] = %+v, want %+v", label, i, got[i], want[i])
				}
			}
		}

		assertLeaderboard("Implementers", rep.Implementers, []ModelCount{
			{Model: "anthropic/claude-sonnet-4", Commits: 7, Percent: 53.85},
			{Model: "google/gemini-2.5-pro", Commits: 6, Percent: 46.15},
		})
		assertLeaderboard("Planners", rep.Planners, []ModelCount{
			{Model: "openai/o3", Commits: 7, Percent: 53.85},
		})
		assertLeaderboard("Orchestrators", rep.Orchestrators, []ModelCount{
			{Model: "anthropic/claude-opus-4", Commits: 1, Percent: 7.69},
		})
		assertLeaderboard("Reviewers", rep.Reviewers, []ModelCount{
			{Model: "openai/gpt-4.1", Commits: 2, Percent: 15.38},
		})
		assertLeaderboard("Committers", rep.Committers, []ModelCount{
			{Model: "google/gemini-2.5-flash", Commits: 1, Percent: 7.69},
		})
		assertLeaderboard("Harnesses", rep.Harnesses, []ModelCount{
			{Model: "pi@1.0.0", Commits: 8, Percent: 61.54},
		})

		// Verify --since matching no commits returns zero report with nil error.
		futureRep, err := Collect(repoDir, Options{Since: "2099-01-01"})
		if err != nil {
			t.Fatalf("Collect with future --since failed: %v", err)
		}
		if futureRep.TotalCommits != 0 || futureRep.TotalLines != 0 {
			t.Errorf("future --since report = %+v, want zero commits and lines", futureRep)
		}
	})
}
