ALTER TABLE domains ADD COLUMN IF NOT EXISTS attachment_s3_bucket text NOT NULL DEFAULT '';
ALTER TABLE domains ADD COLUMN IF NOT EXISTS sending_feedback_ready boolean NOT NULL DEFAULT false;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS sending_setup_error text NOT NULL DEFAULT '';
ALTER TABLE domains ADD COLUMN IF NOT EXISTS sending_setup_at timestamptz;

-- Sending resources are independent of receipt rules and root MX records.
CREATE TABLE sending_configs (
    org_id integer PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    s3_bucket text NOT NULL,
    s3_region text NOT NULL,
    sns_topic_arn text NOT NULL UNIQUE,
    webhook_secret text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE received_emails ADD COLUMN mailbox_owner_id integer;
UPDATE received_emails e SET mailbox_owner_id=i.user_id FROM identities i WHERE i.id=e.identity_id;
CREATE TABLE mailbox_change_counters (
    user_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    cursor bigint NOT NULL DEFAULT 0,
    retained_after bigint NOT NULL DEFAULT 0
);
CREATE TABLE mailbox_changes (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cursor bigint NOT NULL,
    message_uuid uuid NOT NULL,
    identity_id integer NOT NULL,
    domain_id integer NOT NULL,
    operation text NOT NULL CHECK (operation IN ('created','updated','deleted')),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id,cursor)
);
CREATE INDEX mailbox_changes_retention_idx ON mailbox_changes(changed_at);

-- Capture existing messages so a new consumer can start at cursor zero.
INSERT INTO mailbox_changes(user_id,cursor,message_uuid,identity_id,domain_id,operation)
SELECT mailbox_owner_id,row_number() OVER(PARTITION BY mailbox_owner_id ORDER BY id),uuid,identity_id,domain_id,'created'
FROM received_emails WHERE mailbox_owner_id IS NOT NULL;
INSERT INTO mailbox_change_counters(user_id,cursor)
SELECT user_id,max(cursor) FROM mailbox_changes GROUP BY user_id;

CREATE FUNCTION mailat_mailbox_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT user_id INTO NEW.mailbox_owner_id FROM identities WHERE id=NEW.identity_id;
    RETURN NEW;
END;
$$;
CREATE TRIGGER mailbox_owner BEFORE INSERT OR UPDATE OF identity_id ON received_emails
FOR EACH ROW EXECUTE FUNCTION mailat_mailbox_owner();

CREATE FUNCTION mailat_mailbox_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mail received_emails%ROWTYPE; next_cursor bigint;
BEGIN
    IF TG_OP='DELETE' THEN mail:=OLD; ELSE mail:=NEW; END IF;
    IF mail.mailbox_owner_id IS NULL OR NOT EXISTS(SELECT 1 FROM users WHERE id=mail.mailbox_owner_id) THEN
        -- User deletion cascades have no consumer left to retain a tombstone for.
        RETURN NULL;
    END IF;
    -- A counter row lock is held until commit. Unlike a bare sequence, a
    -- consumer cannot observe cursor N+1 before the transaction for N commits.
    INSERT INTO mailbox_change_counters(user_id,cursor) VALUES(mail.mailbox_owner_id,1)
    ON CONFLICT(user_id) DO UPDATE SET cursor=mailbox_change_counters.cursor+1
    RETURNING cursor INTO next_cursor;
    INSERT INTO mailbox_changes(user_id,cursor,message_uuid,identity_id,domain_id,operation)
    VALUES(mail.mailbox_owner_id,next_cursor,mail.uuid,mail.identity_id,mail.domain_id,
           CASE TG_OP WHEN 'INSERT' THEN 'created' WHEN 'DELETE' THEN 'deleted' ELSE 'updated' END);
    RETURN NULL;
END;
$$;
CREATE TRIGGER mailbox_change AFTER INSERT OR UPDATE OR DELETE ON received_emails
FOR EACH ROW EXECUTE FUNCTION mailat_mailbox_change();

-- Import compatible legacy rules, retaining the legacy row and marking it
-- inactive only after an equivalent receiving filter has been created.
ALTER TABLE inbox_filters ADD COLUMN legacy_rule_id integer UNIQUE;
ALTER TABLE email_rules ADD COLUMN migration_warning text;
ALTER TABLE email_rules ALTER COLUMN updated_at SET DEFAULT now();

CREATE FUNCTION mailat_sync_legacy_rule() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE converted jsonb; labels text[]; destination text; why text; identity integer;
BEGIN
    IF pg_trigger_depth()>1 THEN IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF; END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('mailat:labels:'||CASE WHEN TG_OP='DELETE' THEN OLD.user_id ELSE NEW.user_id END::text,0));
    IF TG_OP='DELETE' THEN DELETE FROM inbox_filters WHERE legacy_rule_id=OLD.id; RETURN OLD; END IF;
    IF jsonb_typeof(NEW.conditions) IS DISTINCT FROM 'array' OR jsonb_typeof(NEW.actions) IS DISTINCT FROM 'array' THEN
        IF current_setting('mailat.migrating_rules',true)='yes' THEN
            NEW.active:=false; NEW.migration_warning:='Legacy conditions/actions are not arrays'; RETURN NEW;
        END IF;
        RAISE EXCEPTION 'Conditions and actions must be arrays' USING ERRCODE='22023';
    END IF;
    IF jsonb_array_length(NEW.conditions)=0 OR jsonb_array_length(NEW.conditions)>25
       OR NEW.condition_logic NOT IN ('all','any') THEN why:='Invalid conditions'; END IF;
    IF COALESCE(cardinality(NEW.identity_ids),0)>1 THEN why:='Create one SES filter per identity'; END IF;
    IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.conditions) c WHERE
       COALESCE(c->>'field','') NOT IN ('from','to','subject','body','hasAttachment') OR
       COALESCE(c->>'operator','') NOT IN ('contains','equals','matches','starts_with','ends_with','not_contains','not_equals') OR
       COALESCE(c->>'caseSensitive','false')='true') THEN why:='Unsupported SES condition'; END IF;
    IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.conditions)c WHERE c->>'value' IS NULL OR length(c->>'value')>1000 OR
       (c->>'field'='hasAttachment' AND (c->>'operator'<>'equals' OR c->>'value' NOT IN ('true','false')))) THEN why:='Invalid SES condition value'; END IF;
    IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.actions)a WHERE a->>'type'='add_label' AND
       (COALESCE(btrim(a->>'value'),'')='' OR length(a->>'value')>100)) THEN why:='Invalid SES label name'; END IF;
    IF jsonb_array_length(NEW.actions)=0 OR EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.actions) a
       WHERE COALESCE(a->>'type','') NOT IN ('move_to_folder','add_label','mark_read','mark_starred','delete')) THEN why:='Unsupported SES action; use an external workflow for forwarding or auto-reply'; END IF;
    SELECT a->>'value' INTO destination FROM jsonb_array_elements(NEW.actions) WITH ORDINALITY e(a,n)
       WHERE a->>'type'='move_to_folder' ORDER BY n DESC LIMIT 1;
    IF destination IS NOT NULL AND destination NOT IN ('inbox','archive','spam','trash','dmarc-reports') THEN why:='Invalid SES folder'; END IF;
    identity:=NEW.identity_ids[1];
    IF identity IS NOT NULL AND NOT EXISTS(SELECT 1 FROM identities WHERE id=identity AND user_id=NEW.user_id) THEN why:='Identity is not owned by the rule user'; END IF;
    IF why IS NOT NULL THEN
        IF current_setting('mailat.migrating_rules',true)='yes' THEN
            NEW.active:=false; NEW.migration_warning:=why; RETURN NEW;
        END IF;
        RAISE EXCEPTION '%',why USING ERRCODE='22023';
    END IF;
    SELECT jsonb_agg(jsonb_build_object('field',c->>'field','operator',CASE c->>'operator'
       WHEN 'matches' THEN 'regex' WHEN 'starts_with' THEN 'startsWith' WHEN 'ends_with' THEN 'endsWith'
       WHEN 'not_contains' THEN 'notContains' WHEN 'not_equals' THEN 'notEquals' ELSE c->>'operator' END,'value',c->>'value'))
       INTO converted FROM jsonb_array_elements(NEW.conditions)c;
    SELECT COALESCE(array_agg(DISTINCT a->>'value'),'{}') INTO labels FROM jsonb_array_elements(NEW.actions)a WHERE a->>'type'='add_label';
    INSERT INTO email_labels(org_id,user_id,name,updated_at)
       SELECT NEW.org_id,NEW.user_id,l,NOW() FROM unnest(labels)l ON CONFLICT(user_id,name) DO NOTHING;
    INSERT INTO inbox_filters(legacy_rule_id,org_id,user_id,identity_id,name,priority,active,conditions,condition_logic,action_labels,action_folder,action_star,action_mark_read,action_trash,updated_at)
    VALUES(NEW.id,NEW.org_id,NEW.user_id,identity,NEW.name,NEW.priority,NEW.active,converted,NEW.condition_logic,labels,destination,
       EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.actions)a WHERE a->>'type'='mark_starred'),
       EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.actions)a WHERE a->>'type'='mark_read'),
       EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.actions)a WHERE a->>'type'='delete'),NOW())
    ON CONFLICT(legacy_rule_id) DO UPDATE SET name=EXCLUDED.name,priority=EXCLUDED.priority,active=EXCLUDED.active,
       identity_id=EXCLUDED.identity_id,conditions=EXCLUDED.conditions,condition_logic=EXCLUDED.condition_logic,
       action_labels=EXCLUDED.action_labels,action_folder=EXCLUDED.action_folder,action_star=EXCLUDED.action_star,
       action_mark_read=EXCLUDED.action_mark_read,action_trash=EXCLUDED.action_trash,action_archive=false,updated_at=NOW();
    NEW.migration_warning:=NULL;
    RETURN NEW;
END;
$$;
CREATE TRIGGER legacy_rule_sync BEFORE INSERT OR UPDATE OF name,priority,active,conditions,condition_logic,actions,identity_ids OR DELETE
ON email_rules FOR EACH ROW EXECUTE FUNCTION mailat_sync_legacy_rule();
SELECT set_config('mailat.migrating_rules','yes',true);
UPDATE email_rules SET active=active;
SELECT set_config('mailat.migrating_rules','no',true);

-- Both the compatibility API and the canonical SES filter API edit the same
-- effective rule. Trigger depth prevents recursive synchronization.
CREATE FUNCTION mailat_sync_filter_legacy() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE converted_conditions jsonb; converted_actions jsonb:='[]'; label text; destination text;
BEGIN
    IF pg_trigger_depth()>1 THEN RETURN NULL; END IF;
    IF TG_OP='DELETE' THEN
        IF OLD.legacy_rule_id IS NOT NULL THEN DELETE FROM email_rules WHERE id=OLD.legacy_rule_id; END IF;
        RETURN NULL;
    END IF;
    IF NEW.legacy_rule_id IS NULL THEN RETURN NULL; END IF;
    SELECT jsonb_agg(jsonb_build_object('field',c->>'field','operator',CASE c->>'operator'
       WHEN 'regex' THEN 'matches' WHEN 'startsWith' THEN 'starts_with' WHEN 'endsWith' THEN 'ends_with'
       WHEN 'notContains' THEN 'not_contains' WHEN 'notEquals' THEN 'not_equals' ELSE c->>'operator' END,
       'value',c->>'value','caseSensitive',false)) INTO converted_conditions FROM jsonb_array_elements(NEW.conditions)c;
    destination:=NEW.action_folder;
    IF NEW.action_archive THEN destination:='archive'; END IF;
    IF NEW.action_trash THEN destination:='trash'; END IF;
    IF COALESCE(destination,'')<>'' THEN converted_actions:=converted_actions||jsonb_build_array(jsonb_build_object('type','move_to_folder','value',destination)); END IF;
    IF NEW.action_star THEN converted_actions:=converted_actions||jsonb_build_array(jsonb_build_object('type','mark_starred','value','')); END IF;
    IF NEW.action_mark_read THEN converted_actions:=converted_actions||jsonb_build_array(jsonb_build_object('type','mark_read','value','')); END IF;
    FOREACH label IN ARRAY COALESCE(NEW.action_labels,'{}') LOOP
       converted_actions:=converted_actions||jsonb_build_array(jsonb_build_object('type','add_label','value',label));
    END LOOP;
    UPDATE email_rules SET name=NEW.name,priority=NEW.priority,active=NEW.active,conditions=converted_conditions,
       condition_logic=NEW.condition_logic,actions=converted_actions,identity_ids=CASE WHEN NEW.identity_id IS NULL THEN '{}'::integer[] ELSE ARRAY[NEW.identity_id] END,
       migration_warning=NULL,updated_at=NOW() WHERE id=NEW.legacy_rule_id;
    RETURN NULL;
END;
$$;
CREATE TRIGGER filter_legacy_sync AFTER UPDATE OR DELETE ON inbox_filters
FOR EACH ROW EXECUTE FUNCTION mailat_sync_filter_legacy();
