-- Liste ve pano sorguları için dizinler (ölçümler README'de).
-- Fatura/ödeme listesi: yeniden eskiye keyset sayfalama (business_id sabit, created_at DESC, id DESC).
CREATE INDEX invoices_biz_created_idx ON invoices (business_id, created_at DESC, id DESC);
CREATE INDEX payments_biz_created_idx ON payments (business_id, created_at DESC, id DESC);
-- KDV özeti ve pano: tarih aralığına göre fatura taraması.
CREATE INDEX invoices_biz_issue_idx ON invoices (business_id, issue_date);
-- Cari ve ürün listesi: ada/SKU'ya göre sıralı keyset sayfalama.
CREATE INDEX contacts_biz_name_idx ON contacts (business_id, name, id);
CREATE INDEX products_biz_sku_idx ON products (business_id, sku, id);
