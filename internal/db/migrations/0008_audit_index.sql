-- Denetim kaydı listeleme: işletme bazında yeniden eskiye (id azalan) keyset sayfalama.
CREATE INDEX audit_log_biz_idx ON audit_log (business_id, id DESC);
