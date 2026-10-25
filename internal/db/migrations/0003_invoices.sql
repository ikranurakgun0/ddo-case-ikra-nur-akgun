-- Seri/yıl bazlı sayaç. INSERT ... ON CONFLICT DO UPDATE satırı transaction sonuna kadar
-- kilitler: eşzamanlı istekler sıraya girer, transaction geri alınırsa numara da geri döner.
CREATE TABLE invoice_counters (
    business_id uuid   NOT NULL REFERENCES businesses(id),
    series      text   NOT NULL,
    year        int    NOT NULL,
    last_no     bigint NOT NULL CHECK (last_no >= 0),
    PRIMARY KEY (business_id, series, year)
);

CREATE TABLE invoices (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id        uuid NOT NULL REFERENCES businesses(id),
    kind               text NOT NULL CHECK (kind IN ('sale','purchase')),
    contact_id         uuid NOT NULL,
    series             text NOT NULL CHECK (series ~ '^[A-Z0-9]{1,8}$'),
    year               int  NOT NULL CHECK (year BETWEEN 2000 AND 2100),
    number             bigint NOT NULL CHECK (number > 0),
    issue_date         date NOT NULL,
    due_date           date NOT NULL,
    status             text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued','cancelled')),
    prices_include_vat boolean NOT NULL,
    general_discount   bigint NOT NULL DEFAULT 0 CHECK (general_discount >= 0),
    net_total          bigint NOT NULL CHECK (net_total >= 0),
    vat_total          bigint NOT NULL CHECK (vat_total >= 0),
    gross_total        bigint NOT NULL CHECK (gross_total > 0),
    created_by         uuid REFERENCES users(id),
    created_at         timestamptz NOT NULL DEFAULT now(),
    cancelled_at       timestamptz,
    CHECK (due_date >= issue_date),
    CHECK (net_total + vat_total = gross_total),
    UNIQUE (business_id, series, year, number),
    UNIQUE (id, business_id),
    FOREIGN KEY (contact_id, business_id) REFERENCES contacts(id, business_id)
);
CREATE INDEX invoices_business_idx ON invoices (business_id, id);
CREATE INDEX invoices_contact_idx  ON invoices (business_id, contact_id, issue_date);

CREATE TABLE invoice_lines (
    id                 bigserial PRIMARY KEY,
    invoice_id         uuid     NOT NULL,
    business_id        uuid     NOT NULL,
    line_no            int      NOT NULL CHECK (line_no > 0),
    description        text     NOT NULL CHECK (length(trim(description)) > 0),
    quantity_milli     bigint   NOT NULL CHECK (quantity_milli > 0),   -- 1000 = 1,000 birim
    unit_price         bigint   NOT NULL CHECK (unit_price >= 0),      -- kuruş
    vat_rate           smallint NOT NULL CHECK (vat_rate IN (1, 10, 20)),
    line_discount      bigint   NOT NULL DEFAULT 0 CHECK (line_discount >= 0),
    allocated_discount bigint   NOT NULL DEFAULT 0 CHECK (allocated_discount >= 0),
    net_amount         bigint   NOT NULL CHECK (net_amount >= 0),
    vat_amount         bigint   NOT NULL CHECK (vat_amount >= 0),
    gross_amount       bigint   NOT NULL CHECK (gross_amount >= 0),
    CHECK (net_amount + vat_amount = gross_amount),
    UNIQUE (invoice_id, line_no),
    FOREIGN KEY (invoice_id, business_id) REFERENCES invoices(id, business_id)
);

-- Fatura toplamları satır toplamlarına eşit olmalı (transaction sonunda kontrol edilir).
CREATE FUNCTION assert_invoice_totals() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    iid uuid; inv record; cnt int; n bigint; v bigint; g bigint; d bigint;
BEGIN
    IF TG_TABLE_NAME = 'invoices' THEN iid := NEW.id; ELSE iid := NEW.invoice_id; END IF;
    SELECT net_total, vat_total, gross_total, general_discount INTO inv FROM invoices WHERE id = iid;
    SELECT count(*), coalesce(sum(net_amount),0), coalesce(sum(vat_amount),0),
           coalesce(sum(gross_amount),0), coalesce(sum(allocated_discount),0)
      INTO cnt, n, v, g, d FROM invoice_lines WHERE invoice_id = iid;
    IF cnt = 0 OR n <> inv.net_total OR v <> inv.vat_total OR g <> inv.gross_total OR d <> inv.general_discount THEN
        RAISE EXCEPTION 'fatura toplamları satırlarla uyuşmuyor (satır=%, net=%/%, kdv=%/%, genel=%/%, iskonto=%/%)',
            cnt, n, inv.net_total, v, inv.vat_total, g, inv.gross_total, d, inv.general_discount
            USING ERRCODE = 'DD004';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER invoices_totals AFTER INSERT ON invoices
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_invoice_totals();
CREATE CONSTRAINT TRIGGER invoice_lines_totals AFTER INSERT ON invoice_lines
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_invoice_totals();

-- Faturalar değiştirilemez; tek izinli değişiklik 'issued' -> 'cancelled' geçişidir.
CREATE FUNCTION invoices_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'fatura silinemez; iptal için ters kayıt kullanın' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status = 'issued' AND NEW.status = 'cancelled' AND
       (NEW.id, NEW.business_id, NEW.kind, NEW.contact_id, NEW.series, NEW.year, NEW.number, NEW.issue_date,
        NEW.due_date, NEW.prices_include_vat, NEW.general_discount, NEW.net_total, NEW.vat_total, NEW.gross_total)
       IS NOT DISTINCT FROM
       (OLD.id, OLD.business_id, OLD.kind, OLD.contact_id, OLD.series, OLD.year, OLD.number, OLD.issue_date,
        OLD.due_date, OLD.prices_include_vat, OLD.general_discount, OLD.net_total, OLD.vat_total, OLD.gross_total) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'fatura değiştirilemez (yalnızca issued -> cancelled geçişine izin var)'
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
CREATE TRIGGER invoices_guard_trg BEFORE UPDATE OR DELETE ON invoices
    FOR EACH ROW EXECUTE FUNCTION invoices_guard();
CREATE TRIGGER invoice_lines_immutable BEFORE UPDATE OR DELETE ON invoice_lines
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- İdempotans anahtarları: aynı anahtar + aynı istek -> aynı yanıt.
CREATE TABLE idempotency_keys (
    business_id   uuid  NOT NULL REFERENCES businesses(id),
    key           text  NOT NULL CHECK (length(key) BETWEEN 1 AND 200),
    request_hash  bytea NOT NULL,
    status_code   int,
    response_body jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (business_id, key)
);
