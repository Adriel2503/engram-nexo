---
title: Reintentos HTTP
category: lineamiento
---
# Reintentos HTTP

## Política

Las llamadas HTTP a servicios externos usan la librería Polly con tres reintentos y espera exponencial
(1, 2 y 4 segundos). No se reintentan errores 4xx.

## Circuit breaker

Tras cinco fallas seguidas el circuito se abre durante 30 segundos.
