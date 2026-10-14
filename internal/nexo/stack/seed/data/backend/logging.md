---
title: Logging en microservicios (EJEMPLO)
category: lineamiento
owner: publicador
---
# Logging en microservicios (EJEMPLO)

> Nota: Documento de (EJEMPLO) para pruebas y demostración de Nexo.

## Niveles de log

Usar Information para eventos de negocio, Warning para situaciones recuperables y Error para fallas
que requieren atención. Debug solo en ambientes de desarrollo.

## Correlación

Cada petición lleva un identificador de correlación (X-Correlation-Id) que se propaga a los servicios
llamados y se registra en cada línea de log.

## Datos sensibles

Nunca registrar credenciales, tokens ni datos personales. Enmascarar cualquier identificador sensible.
