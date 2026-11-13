-- Revierte 000007. Se pierde qué etiquetas eran de valor: las métricas
-- vuelven a contar todo lo hecho como valor.

ALTER TABLE issues.labels DROP COLUMN IF EXISTS value;
