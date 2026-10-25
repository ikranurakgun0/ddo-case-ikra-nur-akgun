-- Hesap planına ek hesaplar (yeni ve mevcut işletmeler için)
CREATE OR REPLACE FUNCTION seed_chart_of_accounts(b uuid) RETURNS void LANGUAGE sql AS $$
    INSERT INTO accounts(business_id, code, name, type) VALUES
        (b, '100', 'Kasa',                          'asset'),
        (b, '102', 'Bankalar',                      'asset'),
        (b, '120', 'Alıcılar',                      'asset'),
        (b, '153', 'Ticari Mallar',                 'asset'),
        (b, '159', 'Verilen Sipariş Avansları',     'asset'),
        (b, '191', 'İndirilecek KDV',               'asset'),
        (b, '320', 'Satıcılar',                     'liability'),
        (b, '340', 'Alınan Sipariş Avansları',      'liability'),
        (b, '391', 'Hesaplanan KDV',                'liability'),
        (b, '500', 'Sermaye',                       'equity'),
        (b, '600', 'Yurtiçi Satışlar',              'revenue'),
        (b, '621', 'Satılan Ticari Mallar Maliyeti','expense'),
        (b, '770', 'Genel Yönetim Giderleri',       'expense')
    ON CONFLICT (business_id, code) DO NOTHING;
$$;
SELECT seed_chart_of_accounts(id) FROM businesses;

-- ============ Tahsilat / ödeme ============
CREATE TABLE payments (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id uuid NOT NULL REFERENCES businesses(id),
    kind        text NOT NULL CHECK (kind IN ('receipt','disbursement')),   -- tahsilat | ödeme
    contact_id  uuid NOT NULL,
    method      text NOT NULL CHECK (method IN ('cash','bank')),            -- kasa | banka
    pay_date    date NOT NULL,
    amount      bigint NOT NULL CHECK (amount > 0),
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active','deleted')),
    created_by  uuid REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,
    UNIQUE (id, business_id),
    FOREIGN KEY (contact_id, business_id) REFERENCES contacts(id, business_id)
);
CREATE INDEX payments_contact_idx ON payments (business_id, contact_id, pay_date);

-- Mahsup kayıtları. Silinmez; tahsilat silinince reversed_at doldurulur.
CREATE TABLE payment_allocations (
    id          bigserial PRIMARY KEY,
    business_id uuid   NOT NULL,
    payment_id  uuid   NOT NULL,
    invoice_id  uuid   NOT NULL,
    amount      bigint NOT NULL CHECK (amount > 0),
    created_at  timestamptz NOT NULL DEFAULT now(),
    reversed_at timestamptz,
    FOREIGN KEY (payment_id, business_id) REFERENCES payments(id, business_id),
    FOREIGN KEY (invoice_id, business_id) REFERENCES invoices(id, business_id)
);
CREATE INDEX payment_allocations_invoice_idx ON payment_allocations (invoice_id) WHERE reversed_at IS NULL;
CREATE INDEX payment_allocations_payment_idx ON payment_allocations (payment_id);

-- Aşırı mahsup engeli (transaction sonunda):
--   * bir faturaya mahsup edilen toplam, fatura brütünü aşamaz
--   * bir tahsilattan mahsup edilen toplam, tahsilat tutarını aşamaz
--   * tahsilat türü fatura türüyle ve carisiyle uyumlu olmalı
CREATE FUNCTION assert_allocation_limits() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    inv_gross bigint; inv_kind text; inv_contact uuid; inv_status text;
    pay_amount bigint; pay_kind text; pay_contact uuid;
    inv_sum bigint; pay_sum bigint;
BEGIN
    SELECT gross_total, kind, contact_id, status INTO inv_gross, inv_kind, inv_contact, inv_status FROM invoices WHERE id = NEW.invoice_id;
    SELECT amount, kind, contact_id INTO pay_amount, pay_kind, pay_contact FROM payments WHERE id = NEW.payment_id;
    IF NEW.reversed_at IS NOT NULL THEN RETURN NULL; END IF;
    IF inv_contact <> pay_contact OR (pay_kind = 'receipt' AND inv_kind <> 'sale') OR (pay_kind = 'disbursement' AND inv_kind <> 'purchase') THEN
        RAISE EXCEPTION 'mahsup: ödeme ile fatura cari veya tür bakımından uyumsuz' USING ERRCODE = 'DD005';
    END IF;
    IF inv_status <> 'issued' THEN
        RAISE EXCEPTION 'mahsup: iptal edilmiş faturaya mahsup yapılamaz' USING ERRCODE = 'DD005';
    END IF;
    SELECT coalesce(sum(amount),0) INTO inv_sum FROM payment_allocations WHERE invoice_id = NEW.invoice_id AND reversed_at IS NULL;
    SELECT coalesce(sum(amount),0) INTO pay_sum FROM payment_allocations WHERE payment_id = NEW.payment_id AND reversed_at IS NULL;
    IF inv_sum > inv_gross THEN
        RAISE EXCEPTION 'mahsup: fatura tutarı aşıldı (%/%)', inv_sum, inv_gross USING ERRCODE = 'DD005';
    END IF;
    IF pay_sum > pay_amount THEN
        RAISE EXCEPTION 'mahsup: ödeme tutarı aşıldı (%/%)', pay_sum, pay_amount USING ERRCODE = 'DD005';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER payment_allocations_limits AFTER INSERT ON payment_allocations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION assert_allocation_limits();

-- Değiştirilemezlik: ödeme yalnızca active->deleted; mahsup yalnızca reversed_at boş->dolu.
CREATE FUNCTION payments_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'ödeme silinemez; silme işlemi status=deleted ile yapılır' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status = 'active' AND NEW.status = 'deleted' AND
       (NEW.id, NEW.business_id, NEW.kind, NEW.contact_id, NEW.method, NEW.pay_date, NEW.amount)
       IS NOT DISTINCT FROM (OLD.id, OLD.business_id, OLD.kind, OLD.contact_id, OLD.method, OLD.pay_date, OLD.amount) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'ödeme değiştirilemez' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
CREATE TRIGGER payments_guard_trg BEFORE UPDATE OR DELETE ON payments
    FOR EACH ROW EXECUTE FUNCTION payments_guard();

CREATE FUNCTION allocations_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'mahsup silinemez; geri alma reversed_at ile yapılır' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.reversed_at IS NULL AND NEW.reversed_at IS NOT NULL AND
       (NEW.id, NEW.business_id, NEW.payment_id, NEW.invoice_id, NEW.amount)
       IS NOT DISTINCT FROM (OLD.id, OLD.business_id, OLD.payment_id, OLD.invoice_id, OLD.amount) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'mahsup değiştirilemez' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
CREATE TRIGGER allocations_guard_trg BEFORE UPDATE OR DELETE ON payment_allocations
    FOR EACH ROW EXECUTE FUNCTION allocations_guard();
