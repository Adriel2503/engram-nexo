---
title: Microfrontends (EJEMPLO)
category: lineamiento
owner: publicador
---
# Microfrontends (EJEMPLO)

> Nota: Documento de (EJEMPLO) para pruebas y demostración de Nexo.

## Comunicación con el backend

Cada microfrontend consume su propio BFF. No llama directamente a los microservicios.

## Estado global

El estado compartido entre microfrontends se publica con eventos del navegador, no con variables globales.
