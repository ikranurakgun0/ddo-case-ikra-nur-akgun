// k6 yük testi: eşzamanlı fatura kesimi.
//
// Ölçer: istek gecikmesi (p95), hata oranı ve geçerlilik. Test sonunda (teardown) bu çalıştırmanın
// seri numaralarının 1..N boşluksuz ve çiftsiz olduğunu API'den okuyarak DOĞRULAR; ihlal varsa testi
// başarısız bırakır. Her çalıştırma kendi benzersiz serisini kullanır.
//
// Çalıştırma:  make load        (ya da: docker compose --profile load run --rm k6)
// Ayarlar:     K6_VUS=50 K6_DURATION=60s make load

import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.API_URL || 'http://localhost:8080';
const created = new Counter('invoices_created');

export const options = {
  scenarios: {
    invoices: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 20),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{name:create_invoice}': ['p(95)<1500'],
    checks: ['rate>0.99'],
    invoices_created: ['count>0'],
  },
};

const json = (token) => ({
  headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
});

export function setup() {
  const login = http.post(
    `${BASE}/auth/login`,
    JSON.stringify({ email: 'owner@isletme3.demo', password: 'Demo123!' }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  if (login.status !== 200) fail(`giriş başarısız: ${login.status} ${login.body}`);
  const token = login.json('access_token');

  const series = 'K' + Date.now().toString(36).slice(-6).toUpperCase();
  const c = http.post(
    `${BASE}/contacts`,
    JSON.stringify({ kind: 'customer', name: `k6 yük testi ${series}` }),
    json(token),
  );
  if (c.status !== 201) fail(`cari oluşturulamadı: ${c.status} ${c.body}`);
  return { token, contact: c.json('id'), series };
}

export default function (data) {
  const today = new Date();
  const due = new Date(Date.now() + 30 * 86400000);
  const body = {
    kind: 'sale',
    contact_id: data.contact,
    series: data.series,
    issue_date: today.toISOString().slice(0, 10),
    due_date: due.toISOString().slice(0, 10),
    lines: [
      { description: 'k6 kalem 1', quantity: '2', unit_price: 10000 + (__ITER % 50), vat_rate: 20 },
      { description: 'k6 kalem 2', quantity: '1.5', unit_price: 2500, vat_rate: 10, discount: 100 },
    ],
  };
  const params = json(data.token);
  params.headers['Idempotency-Key'] = `${data.series}-${__VU}-${__ITER}`;
  params.tags = { name: 'create_invoice' };
  const res = http.post(`${BASE}/invoices`, JSON.stringify(body), params);
  const ok = check(res, { 'fatura oluşturuldu (201)': (r) => r.status === 201 });
  if (ok) created.add(1);
}

export function teardown(data) {
  // Bu çalıştırmanın serisindeki tüm faturaları sayfa sayfa oku ve numaraları denetle.
  const seqs = [];
  let after = '';
  for (let page = 0; page < 1000; page++) {
    const url = `${BASE}/invoices?series=${data.series}&limit=200${after ? `&after=${after}` : ''}`;
    const r = http.get(url, json(data.token));
    if (r.status !== 200) fail(`liste okunamadı: ${r.status}`);
    for (const it of r.json('items')) seqs.push(it.seq);
    after = r.json('next_after');
    if (!after) break;
  }
  seqs.sort((a, b) => a - b);
  for (let i = 0; i < seqs.length; i++) {
    if (seqs[i] !== i + 1) {
      fail(`NUMARA İHLALİ: ${i + 1}. fatura numarası ${seqs[i]} (1..${seqs.length} boşluksuz ve çiftsiz olmalı)`);
    }
  }
  console.log(`SONUÇ: seri ${data.series}: ${seqs.length} fatura, numaralar 1..${seqs.length} boşluksuz ve çiftsiz`);
}
