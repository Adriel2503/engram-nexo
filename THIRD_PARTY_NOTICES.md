# Avisos de terceros del fork engram-nexo

Código de terceros portado o adaptado dentro de este fork (además de las
dependencias declaradas en `go.mod`, que conservan sus propias licencias).

## WeKnora (Tencent), licencia MIT

- Origen: https://github.com/Tencent/WeKnora, commit `809bda7` (copia local en
  `nexo_ia/repository/WeKnora`).
- Archivos de origen: `internal/application/service/metric/{precision,recall,mrr,map,ndcg}.go`.
- Archivo del fork: `internal/nexo/kb/eval/metrics.go` (NX-10, entrega 2b, R0).
- Cambios de Nexo: relevancia binaria por ancla de sección (nunca por
  subcadena), MAP dividido por el total de relevantes esperados, ideal de
  nDCG@k con min(relevantes, k), Recall@k como fracción y Hit@k aparte. El
  detalle está en la cabecera del archivo y en `NEXO_CHANGES.md` (NX-10 2b).
- Del resto de WeKnora solo se tomaron ideas (diseño de la cola de indexación,
  troceo de tablas por filas, modo grep, MMR); ese código no se copió.

Texto de la licencia (de `LICENSE` de WeKnora):

```
Copyright (C) 2026 Tencent. All rights reserved.

Terms of the MIT:
--------------------------------------------------------------------
Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```
