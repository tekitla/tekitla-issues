-- 000007: etiquetas de valor para las métricas de flujo (issue 036, protos
-- v0.7.0). counts_as_value = la etiqueta cuenta como valor (p. ej.
-- feature, bug) y separa los cerrados de valor del resto.
--
-- DEFAULT false: es lo que son hoy todas las etiquetas (ninguna está marcada
-- como valor), así que no cambia el significado de los datos existentes.
-- Compatible con el binario de v0.4.0: no lee la columna, y el DEFAULT
-- cubre sus INSERT.

ALTER TABLE issues.labels ADD COLUMN counts_as_value BOOLEAN NOT NULL DEFAULT false;
