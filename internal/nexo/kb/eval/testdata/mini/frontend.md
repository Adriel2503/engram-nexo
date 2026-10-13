---
title: Microfrontends
category: lineamiento
---
# Microfrontends

## Comunicación con el backend

Cada microfrontend consume su propio BFF. No llama directamente a los microservicios.

## Estado global

El estado compartido entre microfrontends se publica con eventos del navegador, no con variables globales.
