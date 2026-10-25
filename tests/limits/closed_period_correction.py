#!/usr/bin/env python3
"""Bilinen sınırlama kanıtı: kapatılmış dönemdeki bir alış faturasını düzeltmek (README, Soru 2).

Çalıştırma (sistem ayakta ve seed bitmiş olmalı; işletme 5 demo hesabını kullanır):
    API_URL=http://localhost:8080 python3 tests/limits/closed_period_correction.py

Bu betik işletme 5'in kapalı dönemini 10 gün ileri KAPATIR (geri alınamaz); `make reset-demo` ile veri ilk haline döner.
Tarihler mevcut kapanışa göre hesaplandığı için betik tekrar tekrar çalıştırılabilir.
Beklenen çıktı: kapalı tarihli düzeltme 409 period_closed; açık tarihli düzeltme 409 insufficient_stock;
negatif stok açıkken 201 ama satışın maliyeti 0'a düşer.
"""
import datetime, json, os, time, urllib.error, urllib.request

B = os.environ.get('API_URL', 'http://localhost:8080')


def call(method, path, body=None, tok=None):
    r = urllib.request.Request(B + path, data=json.dumps(body).encode() if body is not None else None, method=method)
    r.add_header('Content-Type', 'application/json')
    if tok:
        r.add_header('Authorization', 'Bearer ' + tok)
    try:
        x = urllib.request.urlopen(r)
        return x.status, json.loads(x.read() or b'null')
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b'null')


tok = call('POST', '/auth/login', {'email': 'owner@isletme5.demo', 'password': 'Demo123!'})[1]['access_token']
n = str(time.time_ns())[-12:]
_, sup = call('POST', '/contacts', {'kind': 'supplier', 'name': 'Sınırlama Tedarikçi ' + n}, tok)
_, cus = call('POST', '/contacts', {'kind': 'customer', 'name': 'Sınırlama Müşteri ' + n}, tok)
_, p = call('POST', '/products', {'sku': 'LIM-' + n, 'name': 'Sınırlama ürünü', 'kind': 'product', 'vat_rate': 20, 'sale_price': 20000}, tok)
L = lambda q, price: {'product_id': p['id'], 'description': 'x', 'quantity': q, 'unit_price': price, 'vat_rate': 20}
inv = lambda kind, c, d, lines: {'kind': kind, 'contact_id': c, 'issue_date': d, 'due_date': '2026-12-31', 'lines': lines}


def costs():
    _, m = call('GET', '/stock-movements?product_id=' + p['id'], None, tok)
    return [x['value'] for x in m['items'] if x['direction'] == 'out' and x['active']]


closed = datetime.date.fromisoformat(call('GET', '/periods', None, tok)[1]['closed_through'])
d_buy, d_sell, d_close = [(closed + datetime.timedelta(days=k)).isoformat() for k in (1, 8, 10)]
c, pur = call('POST', '/invoices', inv('purchase', sup['id'], d_buy, [L('10', 10000)]), tok)
print(f'alış ({d_buy}):', c)
c, _ = call('POST', '/invoices', inv('sale', cus['id'], d_sell, [L('5', 20000)]), tok)
print(f'satış ({d_sell}):', c, '| satışın maliyeti (kuruş):', costs())
print(f'dönem {d_close} tarihine kadar kapatılıyor:', call('POST', '/periods/close', {'through': d_close}, tok)[0])

c, r = call('POST', '/invoices/%s/correct' % pur['id'], inv('purchase', sup['id'], d_buy, [L('10', 12000)]), tok)
print('düzeltme, kapalı tarihle  ->', c, (r.get('error') or {}).get('code'))
today = time.strftime('%Y-%m-%d')
c, r = call('POST', '/invoices/%s/correct' % pur['id'], inv('purchase', sup['id'], today, [L('10', 12000)]), tok)
print('düzeltme, açık tarihle    ->', c, (r.get('error') or {}).get('code'), '| maliyet değişmedi:', costs())
call('PUT', '/settings', {'allow_negative_stock': True}, tok)
c, r = call('POST', '/invoices/%s/correct' % pur['id'], inv('purchase', sup['id'], today, [L('10', 12000)]), tok)
print('negatif stok AÇIK, açık tarihle ->', c, '| satışın maliyeti (kuruş):', costs(), '(önceden 50000)')
call('PUT', '/settings', {'allow_negative_stock': False}, tok)
