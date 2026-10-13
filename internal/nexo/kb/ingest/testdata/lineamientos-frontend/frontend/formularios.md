---
id: frontend/formularios
title: Formularios en aplicaciones web
category: lineamiento
tags: [frontend, formularios, accesibilidad]
owner: ana
valid_from: 2026-09-01
---

# Formularios en aplicaciones web

Este lineamiento define cómo construimos formularios en las aplicaciones de Real Plaza
(portal de clientes, intranet de tiendas y app de operaciones).

## Validación

Toda validación de formularios se hace en dos capas: en el cliente para dar respuesta inmediata
y en el servidor como fuente de verdad. Nunca se confía solo en la validación del cliente.

### Mensajes de error

- El mensaje aparece debajo del campo, en rojo `#B00020`, con un ícono.
- El texto explica cómo corregir el error: "Ingresa un correo con el formato nombre@dominio.com".
- No se usan alertas emergentes (`alert()`) para errores de validación.

### Librería

Usamos React Hook Form con esquemas de Zod:

```tsx
# Esto no es un título: está dentro de un bloque de código
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";

const schema = z.object({
  correo: z.string().email("Ingresa un correo válido"),
  dni: z.string().regex(/^\d{8}$/, "El DNI tiene 8 dígitos"),
});

export function useClienteForm() {
  return useForm({ resolver: zodResolver(schema) });
}
```

## Campos obligatorios

| Campo | Obligatorio | Validación | Mensaje |
|-------|-------------|------------|---------|
| Correo | Sí | Formato de correo | Ingresa un correo válido |
| DNI | Sí | 8 dígitos | El DNI tiene 8 dígitos |
| Teléfono | No | 9 dígitos, empieza con 9 | El celular tiene 9 dígitos |
| Fecha de nacimiento | No | Mayor de 18 años | Debes ser mayor de edad |

Los campos obligatorios se marcan con un asterisco y la leyenda "Campos obligatorios" al inicio del formulario.

## Ejemplo

Formulario de registro de clientes en el portal.

## Accesibilidad

Cada campo tiene una etiqueta `<label>` asociada con `for`. Los errores se anuncian con `aria-live="polite"`.

## Ejemplo

Formulario de reclamo en el libro de reclamaciones.
