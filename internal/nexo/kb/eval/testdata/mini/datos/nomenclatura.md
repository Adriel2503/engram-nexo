---
title: Nomenclatura de base de datos
category: estandar
---
# Nomenclatura de base de datos

## Tablas

Los nombres de tabla van en singular y en snake_case, por ejemplo cliente_direccion.

## Prefijos de objetos

| Objeto | Prefijo | Ejemplo |
|---|---|---|
| Procedimiento de inserción | usp_ins_ | usp_ins_cliente |
| Procedimiento de actualización | usp_upd_ | usp_upd_cliente |
| Procedimiento de consulta | usp_sel_ | usp_sel_cliente |
| Función escalar | ufn_ | ufn_calcular_igv |
| Vista | vw_ | vw_ventas_mes |
| Índice | ix_ | ix_cliente_documento |
| Llave foránea | fk_ | fk_pedido_cliente |
| Disparador | tr_ | tr_auditoria_cliente |

## Llave primaria

La llave primaria se llama id y es de tipo bigint con identidad.
