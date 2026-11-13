-- Local demonstration accounts only. No production credentials are used.
BEGIN;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO users(name,email,password,is_admin,created_at,updated_at,email_verified_at)
VALUES ('Bruno owner','bruno-owner@example.test',crypt('bruno-local-password',gen_salt('bf',10)),false,now(),now(),now()),
       ('Bruno other','bruno-other@example.test',crypt('bruno-local-password',gen_salt('bf',10)),false,now(),now(),now())
ON CONFLICT(email) DO UPDATE SET password=excluded.password,email_verified_at=excluded.email_verified_at,deleted_at=NULL,updated_at=now();
INSERT INTO user_limits(user_id,access_to,memory_limit,used_bytes)
SELECT id,now()+interval '30 days',104857600,0 FROM users WHERE email IN ('bruno-owner@example.test','bruno-other@example.test')
ON CONFLICT(user_id) DO UPDATE SET access_to=excluded.access_to,memory_limit=excluded.memory_limit;
INSERT INTO invite_codes(code_hash,created_at,expires_at,access_to,memory_limit,used_at)
VALUES (encode(digest(repeat('a',64),'sha256'),'hex'),now(),now()+interval '24 hours',now()+interval '30 days',104857600,NULL)
ON CONFLICT(code_hash) DO UPDATE SET created_at=now(),expires_at=excluded.expires_at,access_to=excluded.access_to,memory_limit=excluded.memory_limit,used_at=NULL;
COMMIT;
