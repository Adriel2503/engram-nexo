package store

import "testing"

// NX-07: retriever, modo y commit de una corrida de evaluación son enumeraciones o referencias validadas.
func TestEvalRunEnumerationsAndCommit(t *testing.T) {
	for _, ok := range []string{"fts", "exact", "hybrid", "hybrid+rerank", "fts+rerank"} {
		if !ValidEvalMode(ok) {
			t.Errorf("modo %q debía aceptarse", ok)
		}
	}
	for _, bad := range []string{"", "otro", "FTS", "hybrid+", "hybrid+otro", "hybrid+rerank+rerank", "texto reservado", "hybrid rerank"} {
		if ValidEvalMode(bad) {
			t.Errorf("modo %q debía rechazarse", bad)
		}
	}
	for _, ok := range []string{"pgfts", "hybrid", "pgfts+rerank", "hybrid+rerank", "fixed", "bm25_v2"} {
		if !ValidEvalRetriever(ok) {
			t.Errorf("retriever %q debía aceptarse", ok)
		}
	}
	for _, bad := range []string{"", "PgFts", "texto libre", "pgfts+", "a+b+c+d+e", "nxa_" + "AbCdEfGhIjKlMnOpQrSt", "con\nsalto"} {
		if ValidEvalRetriever(bad) {
			t.Errorf("retriever %q debía rechazarse", bad)
		}
	}
	for _, ok := range []string{"", "abc1234", "main", "release/v3.4.0-nexo.2", "v1.2.3", "feature/x", "HEAD~2"} {
		if !ValidEvalCommit(ok) {
			t.Errorf("commit %q debía aceptarse", ok)
		}
	}
	for _, bad := range []string{"texto libre con espacios", "postgres://u:p@h/db", "Bearer abcdef", "nxa_AbCdEfGhIjKlMnOpQrStUv", "x" + string(make([]byte, 90))} {
		if ValidEvalCommit(bad) {
			t.Errorf("commit %q debía rechazarse", bad)
		}
	}
}
