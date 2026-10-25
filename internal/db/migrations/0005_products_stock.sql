CREATE TABLE business_settings (
    business_id          uuid PRIMARY KEY REFERENCES businesses(id),
    allow_negative_stock boolean NOT NULL DEFAULT false
);
INSERT INTO business_settings(business_id) SELECT id FROM businesses ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION businesses_bootstrap() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM seed_chart_of_accounts(NEW.id);
    INSERT INTO period_locks(business_id, closed_through) VALUES (NEW.id, DATE '1900-01-01');
    INSERT INTO business_settings(business_id) VALUES (NEW.id);
    RETURN NEW;
END;
$$;

CREATE TABLE products (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id uuid NOT NULL REFERENCES businesses(id),
    sku         text NOT NULL CHECK (length(trim(sku)) > 0),
    name        text NOT NULL CHECK (length(trim(name)) > 0),
    kind        text NOT NULL CHECK (kind IN ('product','service')),   -- hizmet kartlarında stok tutulmaz
    unit        text NOT NULL DEFAULT 'adet',
    vat_rate    smallint NOT NULL CHECK (vat_rate IN (1,10,20)),
    sale_price  bigint NOT NULL DEFAULT 0 CHECK (sale_price >= 0),     -- kuruş
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (business_id, sku),
    UNIQUE (id, business_id)
);

ALTER TABLE invoice_lines ADD COLUMN product_id uuid;
ALTER TABLE invoice_lines ADD FOREIGN KEY (product_id, business_id) REFERENCES products(id, business_id);

-- Stok hareketleri. 'value': alışta satır net tutarı (kuruş), satışta hesaplanan maliyet.
-- Maliyet yeniden hesaplandığında yalnızca value (ve iptalde active) güncellenebilir;
-- her değişiklik audit_log'a eski/yeni değerle yazılır ve SMM düzeltme kaydı atılır.
CREATE TABLE stock_movements (
    id             bigserial PRIMARY KEY,
    business_id    uuid   NOT NULL,
    product_id     uuid   NOT NULL,
    direction      text   NOT NULL CHECK (direction IN ('in','out')),
    movement_date  date   NOT NULL,
    quantity_milli bigint NOT NULL CHECK (quantity_milli > 0),
    value          bigint NOT NULL DEFAULT 0,
    active         boolean NOT NULL DEFAULT true,
    invoice_id     uuid   NOT NULL,
    line_no        int    NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (product_id, business_id) REFERENCES products(id, business_id),
    FOREIGN KEY (invoice_id, business_id) REFERENCES invoices(id, business_id),
    UNIQUE (invoice_id, line_no)
);
CREATE INDEX stock_movements_product_idx ON stock_movements (business_id, product_id, movement_date, id);

CREATE FUNCTION stock_movements_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'stok hareketi silinemez' USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF (NEW.id, NEW.business_id, NEW.product_id, NEW.direction, NEW.movement_date, NEW.quantity_milli, NEW.invoice_id, NEW.line_no)
         IS NOT DISTINCT FROM
       (OLD.id, OLD.business_id, OLD.product_id, OLD.direction, OLD.movement_date, OLD.quantity_milli, OLD.invoice_id, OLD.line_no)
       AND (OLD.direction = 'out' OR NEW.value = OLD.value)      -- alış değeri sabit; yalnızca satış maliyeti yeniden hesaplanır
       AND (OLD.active OR NOT NEW.active)                         -- iptal geri alınamaz
    THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'stok hareketi değiştirilemez' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
CREATE TRIGGER stock_movements_guard_trg BEFORE UPDATE OR DELETE ON stock_movements
    FOR EACH ROW EXECUTE FUNCTION stock_movements_guard();
CREATE TRIGGER stock_movements_no_truncate BEFORE TRUNCATE ON stock_movements
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
