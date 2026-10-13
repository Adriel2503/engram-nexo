// Nexo IA · NX-10 R2 (RC-11) — fusión RRF ponderada y normalizada.
//
// Derivado de WeKnora, internal/application/service/knowledgebase_search_fusion.go
// (fuseWithRRF y bestRanks), commit 809bda7, https://github.com/Tencent/WeKnora.
// Adaptado a los tipos de Nexo (sección, una lista por motor).
//
// Copyright (C) 2026 Tencent. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package search

import "sort"

// RRFWeights son los pesos de la fusión (design §20.4): K suaviza la
// diferencia entre posiciones (60, como WeKnora y el artículo original),
// Vector y Text pesan cada lista.
type RRFWeights struct {
	K      int     `json:"k"`
	Vector float64 `json:"vector"`
	Text   float64 `json:"text"`
}

// fused es un candidato de la fusión.
type fused struct {
	id         string
	score      float64
	vectorRank int // 0 = no está en la lista
	textRank   int
}

// fuseRRF fusiona dos listas ordenadas de ids (mejor primero):
//
//	score = (wv/(k+rango_v) + wt/(k+rango_t)) / ((wv+wt)/(k+1))
//
// El rango de cada sección es su mejor posición en cada lista (una sección
// repetida conserva la primera). La división deja en 1 a la que es primera
// en ambas listas, así el puntaje es comparable entre consultas. Desempate
// estable: mejor rango de texto y luego id.
func fuseRRF(vectorIDs, textIDs []string, w RRFWeights) []fused {
	k := w.K
	if k <= 0 {
		k = 60
	}
	wv, wt := w.Vector, w.Text
	if wv <= 0 && wt <= 0 {
		wv, wt = 0.5, 0.5
	}
	maxRRF := (wv + wt) / float64(k+1)
	byID := map[string]*fused{}
	var order []*fused
	add := func(ids []string, vector bool) {
		for i, id := range ids {
			f, ok := byID[id]
			if !ok {
				f = &fused{id: id}
				byID[id] = f
				order = append(order, f)
			}
			if vector && f.vectorRank == 0 {
				f.vectorRank = i + 1
			}
			if !vector && f.textRank == 0 {
				f.textRank = i + 1
			}
		}
	}
	add(vectorIDs, true)
	add(textIDs, false)
	out := make([]fused, 0, len(order))
	for _, f := range order {
		s := 0.0
		if f.vectorRank > 0 {
			s += wv / float64(k+f.vectorRank)
		}
		if f.textRank > 0 {
			s += wt / float64(k+f.textRank)
		}
		f.score = s / maxRRF
		out = append(out, *f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		ti, tj := out[i].textRank, out[j].textRank
		if ti == 0 {
			ti = 1 << 30
		}
		if tj == 0 {
			tj = 1 << 30
		}
		if ti != tj {
			return ti < tj
		}
		return out[i].id < out[j].id
	})
	return out
}
