-- ============ Cariler ============
CREATE TABLE contacts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id uuid NOT NULL REFERENCES businesses(id),
    kind        text NOT NULL CHECK (kind IN ('customer','supplier','both')),
    name        text NOT NULL CHECK (length(trim(name)) > 0),
    tax_no      text,
    email       text,
    phone       text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, business_id)
);
CREATE INDEX contacts_business_idx ON contacts (business_id, id);
ALTER TABLE users ADD CONSTRAINT users_contact_fk FOREIGN KEY (contact_id) REFERENCES contacts(id);

-- ============ Hesap planı ============
CREATE TABLE accounts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id uuid NOT NULL REFERENCES businesses(id),
    code        text NOT NULL,
    name        text NOT NULL,
    type        text NOT NULL CHECK (type IN ('asset','liability','equity','revenue','expense')),
    UNIQUE (business_id, code),
    UNIQUE (id, business_id)
);

CREATE FUNCTION seed_chart_of_accounts(b uuid) RETURNS void LANGUAGE sql AS $$
    INSERT INTO accounts(business_id, code, name, type) VALUES
        (b, '100', 'Kasa',                          'asset'),
        (b, '102', 'Bankalar',                      'asset'),
        (b, '120', 'Alıcılar',                      'asset'),
        (b, '153', 'Ticari Mallar',                 'asset'),
        (b, '191', 'İndirilecek KDV',               'asset'),
        (b, '320', 'Satıcılar',                     'liability'),
        (b, '340', 'Alınan Avanslar',               'liability'),
        (b, '391', 'Hesaplanan KDV',                'liability'),
        (b, '500', 'Sermaye',                       'equity'),
        (b, '600', 'Yurtiçi Satışlar',              'revenue'),
        (b, '621', 'Satılan Ticari Mallar Maliyeti','expense');
$$;

-- ============ Dönem kilidi ============
-- Her işletmenin tek satırı olur; kapatılan dönemin son günü tutulur ve yalnızca ileri gider.
CREATE TABLE period_locks (
    business_id    uuid PRIMARY KEY REFERENCES businesses(id),
    closed_through date NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION period_locks_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'dönem kilidi silinemez' USING ERRCODE = 'DD003';
    END IF;
    IF NEW.closed_through < OLD.closed_through THEN
        RAISE EXCEPTION 'kapatılmış dönem yeniden açılamaz' USING ERRCODE = 'DD003';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER period_locks_guard_trg BEFORE UPDATE OR DELETE ON period_locks
    FOR EACH ROW EXECUTE FUNCTION period_locks_guard();

-- Yeni işletme: hesap planı + açık dönem kilidi satırı otomatik oluşur.
CREATE FUNCTION businesses_bootstrap() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM seed_chart_of_accounts(NEW.id);
    INSERT INTO period_locks(business_id, closed_through) VALUES (NEW.id, DATE '1900-01-01');
    RETURN NEW;
END;
$$;
CREATE TRIGGER businesses_bootstrap_trg AFTER INSERT ON businesses
    FOR EACH ROW EXECUTE FUNCTION businesses_bootstrap();

-- ============ Yevmiye ============
CREATE TABLE journal_entries (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id uuid NOT NULL REFERENCES businesses(id),
    entry_date  date NOT NULL,
    description text NOT NULL,
    source_type text NOT NULL CHECK (source_type IN ('invoice','payment','reversal','manual','stock')),
    source_id   uuid,
    reverses_id uuid UNIQUE REFERENCES journal_entries(id),   -- bir kayıt en fazla bir kez ters çevrilir
    created_by  uuid REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, business_id)
);
CREATE INDEX journal_entries_biz_date_idx ON journal_entries (business_id, entry_date, id);
CREATE INDEX journal_entries_source_idx ON journal_entries (business_id, source_type, source_id);

CREATE TABLE journal_lines (
    id          bigserial PRIMARY KEY,
    entry_id    uuid   NOT NULL,
    business_id uuid   NOT NULL,
    account_id  uuid   NOT NULL,
    contact_id  uuid,
    debit       bigint NOT NULL DEFAULT 0 CHECK (debit  >= 0),   -- kuruş
    credit      bigint NOT NULL DEFAULT 0 CHECK (credit >= 0),   -- kuruş
    CHECK ((debit > 0) <> (credit > 0)),                          -- her satır tam olarak bir taraflı
    -- Bileşik yabancı anahtarlar: başka işletmenin hesabı/carisi/kaydı asla bağlanamaz.
    FOREIGN KEY (entry_id,   business_id) REFERENCES journal_entries(id, business_id),
    FOREIGN KEY (account_id, business_id) REFERENCES accounts(id, business_id),
    FOREIGN KEY (contact_id, business_id) REFERENCES contacts(id, business_id)
);
CREATE INDEX journal_lines_entry_idx   ON journal_lines (entry_id);
CREATE INDEX journal_lines_account_idx ON journal_lines (business_id, account_id);
CREATE INDEX journal_lines_contact_idx ON journal_lines (business_id, contact_id) WHERE contact_id IS NOT NULL;

-- Borç = alacak kuralı: transaction SONUNDA (deferred) her kayıt için kontrol edilir.
CREATE FUNCTION assert_entry_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    eid uuid; d bigint; c bigint; n int;
BEGIN
    IF TG_TABLE_NAME = 'journal_entries' THEN eid := NEW.id; ELSE eid := NEW.entry_id; END IF;
    SELECT coalesce(sum(debit),0), coalesce(sum(credit),0), count(*) INTO d, c, n
      FROM journal_lines WHERE entry_id = eid;
    IF n < 2 OR d <> c THEN
        RAISE EXCEPTION 'yevmiye kaydı dengesiz veya eksik (borç=%, alacak=%, satır=%)', d, c, n
            USING ERRCODE = 'DD002';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER journal_entries_balanced AFTER INSERT ON journal_entries
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_entry_balanced();
CREATE CONSTRAINT TRIGGER journal_lines_balanced AFTER INSERT ON journal_lines
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_entry_balanced();

-- Değiştirilemezlik: yevmiye kayıtları ve satırları UPDATE/DELETE/TRUNCATE edilemez.
CREATE TRIGGER journal_entries_immutable BEFORE UPDATE OR DELETE ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER journal_lines_immutable BEFORE UPDATE OR DELETE ON journal_lines
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER journal_entries_no_truncate BEFORE TRUNCATE ON journal_entries
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER journal_lines_no_truncate BEFORE TRUNCATE ON journal_lines
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- Kapatılmış döneme kayıt girilemez. FOR SHARE: dönem kapatma (UPDATE) ile eşzamanlı
-- kayıt girişi arasında yarış olmasın diye kilit satırı paylaşımlı kilitlenir.
CREATE FUNCTION enforce_open_period() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE closed date;
BEGIN
    SELECT closed_through INTO closed FROM period_locks WHERE business_id = NEW.business_id FOR SHARE;
    IF closed IS NOT NULL AND NEW.entry_date <= closed THEN
        RAISE EXCEPTION 'dönem kapalı: % tarihine kadar kapatılmış, kayıt tarihi %', closed, NEW.entry_date
            USING ERRCODE = 'DD001';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER journal_entries_open_period BEFORE INSERT ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION enforce_open_period();
