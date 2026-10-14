---
title: Ventanas de mantenimiento (EJEMPLO)
category: lineamiento
owner: publicador
---
# Ventanas de mantenimiento (EJEMPLO)

> Nota: Documento de (EJEMPLO) para pruebas y demostración de Nexo.

## Horarios autorizados

Las ventanas estándar de mantenimiento para despliegues con potencial impacto se ejecutan los días martes y jueves entre 01:00 y 04:00 horas (hora local).

## Requisitos de aprobación

- Documento de pase a producción con plan de reversión detallado y validado.
- Pruebas de humo ejecutadas satisfactoriamente en ambiente de ensayo previo.
- Aprobación formal del responsable de operaciones y líder de plataforma.

## Criterios de reversión

Si la validación post-despliegue no concluye exitosamente antes de las 03:30 horas, se ejecuta automáticamente el plan de reversión integral.
