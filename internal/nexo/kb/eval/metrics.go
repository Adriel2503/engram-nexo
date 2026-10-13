// Métricas de recuperación portadas de WeKnora y adaptadas a Nexo (NX-10
// entrega 2b, R0, RC-01).
//
// Origen: github.com/Tencent/WeKnora, commit 809bda7,
// internal/application/service/metric/{precision,recall,mrr,map,ndcg}.go.
//
// Copyright (C) 2026 Tencent. All rights reserved.
//
// MIT License
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//
// Cambios de Nexo:
//   - Entrada: un vector de relevancia binaria por posición (rel[i] = el
//     resultado i coincide, por ancla de sección, con un esperado todavía no
//     acertado) y el total de esperados; nunca por subcadena de texto.
//   - MAP: la precisión media se divide por el total de relevantes esperados
//     (WeKnora divide por los encontrados, lo que premia encontrar pocos).
//   - nDCG@k: el ideal usa min(relevantes, k) (WeKnora usa la cantidad de
//     resultados devueltos, que infla el valor cuando se devuelven pocos).
//   - Recall@k: fracción de esperados entre los k primeros; HitAtK: al menos
//     uno (es el "Recall@5" de design §14.5 y de la medición de 2a).

package eval

import "math"

// HitAtK: 1 si algún resultado relevante está entre los k primeros.
func HitAtK(rel []bool, k int) float64 {
	for i := 0; i < len(rel) && i < k; i++ {
		if rel[i] {
			return 1
		}
	}
	return 0
}

// RecallAtK: relevantes encontrados entre los k primeros / esperados.
func RecallAtK(rel []bool, k, totalRelevant int) float64 {
	if totalRelevant <= 0 {
		return 0
	}
	hits := 0
	for i := 0; i < len(rel) && i < k; i++ {
		if rel[i] {
			hits++
		}
	}
	return float64(hits) / float64(totalRelevant)
}

// PrecisionAtK: relevantes entre los k primeros / resultados considerados.
func PrecisionAtK(rel []bool, k int) float64 {
	n := min(len(rel), k)
	if n == 0 {
		return 0
	}
	hits := 0
	for i := 0; i < n; i++ {
		if rel[i] {
			hits++
		}
	}
	return float64(hits) / float64(n)
}

// ReciprocalRank: 1/posición del primer relevante (0 si no hay).
func ReciprocalRank(rel []bool) float64 {
	for i, r := range rel {
		if r {
			return 1 / float64(i+1)
		}
	}
	return 0
}

// AveragePrecision: suma de la precisión en cada posición relevante dividida
// por el total de relevantes esperados (corrección de Nexo).
func AveragePrecision(rel []bool, totalRelevant int) float64 {
	if totalRelevant <= 0 {
		return 0
	}
	var ap float64
	hits := 0
	for i, r := range rel {
		if r {
			hits++
			ap += float64(hits) / float64(i+1)
		}
	}
	return ap / float64(totalRelevant)
}

// NDCGAtK con relevancia binaria; el ideal pone min(relevantes, k) aciertos
// al principio.
func NDCGAtK(rel []bool, k, totalRelevant int) float64 {
	var dcg float64
	for i := 0; i < len(rel) && i < k; i++ {
		if rel[i] {
			dcg += 1 / math.Log2(float64(i+2))
		}
	}
	var idcg float64
	for i := 0; i < min(totalRelevant, k); i++ {
		idcg += 1 / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}
