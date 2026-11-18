-- Revierte 000009. Se pierden las sesiones de agentes (las vuelve a llenar
-- el recolector si se aplica otra vez). Los tokens pierden sessions:write:
-- un token que solo tenía ese permiso se borra (ya no serviría para nada y
-- el CHECK de 000005 no acepta scopes vacíos); los demás conservan el resto.

DROP TABLE IF EXISTS issues.agent_sessions;

DELETE FROM issues.tokens WHERE scopes = ARRAY['sessions:write']::TEXT[];
UPDATE issues.tokens SET scopes = array_remove(scopes, 'sessions:write')
 WHERE 'sessions:write' = ANY (scopes);

ALTER TABLE issues.tokens DROP CONSTRAINT tokens_scopes_chk;
ALTER TABLE issues.tokens ADD CONSTRAINT tokens_scopes_chk CHECK (cardinality(scopes) > 0
    AND scopes <@ ARRAY['issues:read', 'issues:write']::TEXT[]);
