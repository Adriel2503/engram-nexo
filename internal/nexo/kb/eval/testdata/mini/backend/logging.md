---
title: Logging en microservicios
category: lineamiento
owner: ana
---
# Logging en microservicios

## Niveles de log

Usar Information para eventos de negocio, Warning para situaciones recuperables y Error para fallas
que requieren atención. Debug solo en ambientes de desarrollo.

## Correlación

Cada petición lleva un identificador de correlación (X-Correlation-Id) que se propaga a los servicios
llamados y se registra en cada línea de log.

## Datos sensibles

Nunca registrar contraseñas, tokens ni datos personales. Enmascarar el número de documento.
