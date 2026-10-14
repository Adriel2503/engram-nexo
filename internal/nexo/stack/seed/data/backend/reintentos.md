---
title: Reintentos HTTP (EJEMPLO)
category: lineamiento
owner: publicador
---
# Reintentos HTTP (EJEMPLO)

> Nota: Documento de (EJEMPLO) para pruebas y demostración de Nexo.

## Política

Las llamadas HTTP a servicios externos usan políticas con tres reintentos y espera exponencial
(1, 2 y 4 segundos). No se reintentan errores 4xx.

## Circuit breaker

Tras cinco fallas seguidas el circuito se abre durante 30 segundos.
