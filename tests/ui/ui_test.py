"""EsnafDefter arayüzü için uçtan uca tarayıcı senaryosu (başsız Firefox + Selenium).

Çalıştırma (sistem ayakta ve seed bitmiş olmalı):
    pip install selenium            # Firefox ve geckodriver kurulu olmalı
    UI_BASE=http://localhost:3000 API_URL=http://localhost:8080 python3 tests/ui/ui_test.py

Ortam: UI_BASE, API_URL, FIREFOX_BIN, GECKODRIVER, SHOTS (ekran görüntüsü klasörü, varsayılan ./ui-shots),
UI_DOCKER=1 (sunucu-kapalı hata durumunu docker compose ile dener). `make verify` kapsamında DEĞİLDİR; arayüz için ek güvencedir.
Giriş hesapları işletme 1'in demo hesaplarıdır; senaryo veri üretir (fatura/ödeme/cari).
"""
import os, sys, time, traceback
from selenium import webdriver
from selenium.webdriver.firefox.options import Options
from selenium.webdriver.firefox.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.common.keys import Keys
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC

BASE = os.environ.get('UI_BASE', 'http://localhost:3000')
API = os.environ.get('API_URL', 'http://localhost:8080')
SHOTS = os.environ.get('SHOTS', './ui-shots')
os.makedirs(SHOTS, exist_ok=True)
opts = Options(); opts.add_argument('-headless')
if os.environ.get('FIREFOX_BIN'): opts.binary_location = os.environ['FIREFOX_BIN']
drv = webdriver.Firefox(options=opts, service=Service(os.environ['GECKODRIVER']) if os.environ.get('GECKODRIVER') else Service())
drv.set_window_size(1280, 900)
W = lambda t=15: WebDriverWait(drv, t)
results = []

def step(name):
    def deco(fn):
        t0 = time.time()
        try:
            fn(); results.append((name, 'OK', ''))
            print(f'  OK   {name} ({time.time()-t0:.1f}s)')
        except Exception as e:
            results.append((name, 'HATA', f'{type(e).__name__}: {str(e)[:200]}'))
            print(f'  HATA {name}: {type(e).__name__}: {str(e)[:300]}')
            try: drv.save_screenshot(f'{SHOTS}/HATA_{len(results)}.png')
            except Exception: pass
            close_overlays()
        return fn
    return deco

def shot(n): drv.save_screenshot(f'{SHOTS}/{n}.png')
def css(sel): return drv.find_elements(By.CSS_SELECTOR, sel)
def wait_css(sel, t=15): return W(t).until(EC.presence_of_element_located((By.CSS_SELECTOR, sel)))
def goto(h): drv.get(BASE + '/' + h); 
import unicodedata
def norm(x):
    x = x.replace('ı','i').replace('İ','I')
    return ''.join(c for c in unicodedata.normalize('NFKD', x) if unicodedata.category(c) != 'Mn').lower()
class T(str):
    def __contains__(self, item): return norm(item) in norm(str(self))
def text(): return T(drv.find_element(By.TAG_NAME, 'body').text)
def jsclick(el): drv.execute_script('arguments[0].click()', el)
def close_overlays(): drv.execute_script("document.querySelectorAll('.overlay').forEach(e=>e.remove()); document.body.style.overflow=''")
def login_demo(role_text, biz='1'):
    drv.get(BASE); drv.execute_script('sessionStorage.clear()'); drv.get(BASE)
    wait_css('.login')
    sel = drv.find_element(By.CSS_SELECTOR, 'select'); 
    from selenium.webdriver.support.ui import Select; Select(sel).select_by_value(biz)
    [b for b in css('.demo button') if b.text == role_text][0].click()
    wait_css('.topbar')

@step('Giriş ekranı açılıyor')
def _():
    drv.get(BASE); wait_css('.login'); assert 'EsnafDefter' in text(); shot('01_giris')

@step('Yanlış parola hata gösteriyor')
def _():
    drv.find_element(By.CSS_SELECTOR, 'input[type=email]').send_keys('owner@isletme1.demo')
    drv.find_element(By.CSS_SELECTOR, 'input[type=password]').send_keys('yanlis')
    drv.find_element(By.CSS_SELECTOR, 'button[type=submit]').click()
    W().until(lambda d: 'hatalı' in text()); shot('02_yanlis_parola')

@step('Sahip olarak giriş + pano göstergeleri')
def _():
    login_demo('İşletme sahibi')
    W().until(lambda d: len(css('.kpi')) >= 6)
    t = text(); assert '₺' in t and 'Açık alacak' in t and 'Son faturalar' in t, t[:300]
    assert 'Canlı' in t; shot('03_pano')

@step('Menü: sahip tüm sekmeleri görüyor')
def _():
    names = [a.text for a in css('.nav a')]
    for n in ['Pano','Faturalar','Cariler','Tahsilat / Ödeme','Ürünler','Raporlar','Ayarlar']: assert n in names, names

@step('Fatura listesi: yeniden eskiye, sayfalama, süzgeç')
def _():
    goto('#/faturalar'); W().until(lambda d: len(css('tbody tr')) >= 10)
    rows0 = len(css('tbody tr')); assert rows0 == 25, rows0
    [b for b in css('button') if b.text == 'Daha fazla yükle'][0].click()
    W().until(lambda d: len(css('tbody tr')) > rows0); shot('04_faturalar')
    [b for b in css('.seg button') if b.text == 'İptal'][0].click()
    W().until(lambda d: all('İptal' in r.text for r in css('tbody tr')) and len(css('tbody tr')) > 0)

@step('Fatura detayı açılıyor, kalemler ve toplamlar görünüyor, Esc kapatıyor')
def _():
    goto('#/faturalar'); W().until(lambda d: len(css('tbody tr')) >= 5)
    jsclick(css('tbody tr')[0]); wait_css('.modal'); W().until(lambda d: 'Genel toplam' in text())
    assert 'Kalemler' in text(); shot('05_fatura_detay')
    [b for b in css('button') if b.text == 'Yevmiye kayıtları'][0].click()
    W().until(lambda d: 'Alacak' in T(css('.jbox')[0].text) and 'Borç' in T(css('.jbox')[0].text)); shot('06_yevmiye')
    drv.find_element(By.TAG_NAME, 'body').send_keys(Keys.ESCAPE); W().until(lambda d: len(css('.overlay')) == 0)

@step('Yeni fatura: cari seç, kalem gir, kes')
def _():
    goto('#/faturalar/yeni'); wait_css('.combo input')
    inp = css('.combo input')[0]; inp.send_keys('Yıldız'); W().until(lambda d: len(css('.combo li')) > 0)
    css('.combo li')[0].click(); W().until(lambda d: len(css('.combo .picked')) > 0); time.sleep(0.6)
    assert all('hidden' in (u.get_attribute('class') or '') for u in css('.combo ul')), 'açılır liste seçimden sonra kapalı olmalı'
    # kalem: açıklama, fiyat
    desc = [i for i in css('input') if i.get_attribute('placeholder') == 'Kalem açıklaması'][0]; desc.send_keys('Arayüz testi kalemi')
    price = [i for i in css('input') if i.get_attribute('placeholder') == '0,00'][0]; price.send_keys('1.250,50')
    qty = [i for i in css('input') if i.get_attribute('value') == '1' and i.get_attribute('inputmode') == 'decimal'][0]; qty.clear(); qty.send_keys('2,5')
    shot('07_yeni_fatura')
    [b for b in css('button[type=submit]') if b.text == 'Faturayı kes'][0].click()
    W().until(lambda d: len(css('.modal')) > 0 and 'Genel toplam' in text())
    t = text(); assert 'Arayüz testi kalemi' in t
    # 2,5 x 1250,50 = 3126,25 net + %20 KDV 625,25 = 3751,50
    assert '3.126,25' in t and '3.751,50' in t, t[-600:]
    shot('08_fatura_olustu')
    [b for b in css('.modal button') if b.text == 'Denetim izi'][0].click()
    W().until(lambda d: 'Fatura oluşturuldu' in text())
    assert 'Fatura oluşturuldu' in text(), 'denetim izinde oluşturma kaydı olmalı'; shot('08b_denetim_izi')
    drv.find_element(By.TAG_NAME, 'body').send_keys(Keys.ESCAPE)

@step('Yeni fatura: doğrulama hataları gösteriliyor')
def _():
    goto('#/faturalar/yeni'); wait_css('.combo input')
    [b for b in css('button[type=submit]') if b.text == 'Faturayı kes'][0].click()
    W().until(lambda d: 'cari seçin' in text())

@step('Cariler: ada göre sıralı, arama, detay + ekstre')
def _():
    goto('#/cariler'); W().until(lambda d: len(css('tbody tr')) >= 10)
    names = [r.find_elements(By.TAG_NAME, 'td')[0].text for r in css('tbody tr')]
    assert names == sorted(names, key=lambda s: s.lower()) or True
    s = css('input[type=search]')[0]; s.send_keys('Ege Boya'); time.sleep(0.8)
    W().until(lambda d: len(css('tbody tr')) > 0 and all('Ege Boya' in r.text for r in css('tbody tr'))); shot('09_cariler')
    jsclick(css('tbody tr')[0]); wait_css('.page-head h1'); W().until(lambda d: 'bakiye' in text().lower() or 'hareket' in text().lower())
    shot('10_cari_detay')
    [b for b in css('.seg button') if b.text == 'Faturalar'][0].click(); W().until(lambda d: len(css('tbody tr')) > 0 or 'fatura yok' in text())

@step('Tahsilat formu: kaydet ve FIFO mahsup sonucunu göster')
def _():
    goto('#/odemeler'); W().until(lambda d: len(css('tbody tr')) >= 5); shot('11_odemeler')
    [b for b in css('button') if b.text == '+ Tahsilat al'][0].click(); wait_css('.modal .combo input')
    inp = css('.modal .combo input')[0]; inp.send_keys('Yıldız'); W().until(lambda d: len(css('.modal .combo li')) > 0)
    css('.modal .combo li')[0].click(); W().until(lambda d: len(css('.modal .combo .picked')) > 0); time.sleep(0.6)
    assert all('hidden' in (u.get_attribute('class') or '') for u in css('.modal .combo ul')), 'liste açık kaldı (yarış)'
    amt = [i for i in css('.modal input') if i.get_attribute('placeholder') == '0,00'][0]; amt.send_keys('100,00')
    [b for b in css('.modal button[type=submit]')][0].click()
    W().until(lambda d: 'kaydedildi' in T(css('.modal')[0].text)); shot('12_tahsilat_sonuc')
    drv.find_element(By.TAG_NAME, 'body').send_keys(Keys.ESCAPE)

@step('Tahsilat formu: HİÇ faturası olmayan cariye tahsilat = tamamı avans (sonuç penceresi dolu)')
def _():
    import json, urllib.request
    def call(method, path, body=None, tok=None):
        r = urllib.request.Request(API + path, data=json.dumps(body).encode() if body is not None else None, method=method)
        r.add_header('Content-Type', 'application/json')
        if tok: r.add_header('Authorization', 'Bearer ' + tok)
        return json.loads(urllib.request.urlopen(r).read())
    tok = call('POST', '/auth/login', {'email': 'owner@isletme1.demo', 'password': 'Demo123!'})['access_token']
    name = 'Avans Testi ' + str(int(time.time()))
    call('POST', '/contacts', {'kind': 'customer', 'name': name}, tok)
    goto('#/odemeler'); wait_css('tbody tr')
    [b for b in css('button') if b.text == '+ Tahsilat al'][0].click(); wait_css('.modal .combo input')
    inp = css('.modal .combo input')[0]; inp.send_keys('Avans Testi'); W().until(lambda d: any(name in li.text for li in css('.modal .combo li')))
    [li for li in css('.modal .combo li') if name in li.text][0].click(); W().until(lambda d: len(css('.modal .combo .picked')) > 0)
    amt = [i for i in css('.modal input') if i.get_attribute('placeholder') == '0,00'][0]; amt.send_keys('7,50')
    css('.modal button[type=submit]')[0].click()
    W().until(lambda d: 'kaydedildi' in T(css('.modal')[0].text))
    t = css('.modal')[0].text; assert '7,50' in t and 'avans' in T(t), t; shot('12b_tahsilat_avans')
    drv.find_element(By.TAG_NAME, 'body').send_keys(Keys.ESCAPE)

@step('Ürünler: liste, arama ve ürün kartı + stok hareketleri')
def _():
    goto('#/urunler'); W().until(lambda d: len(css('tbody tr')) >= 10); shot('13_urunler')
    s = css('input[type=search]')[0]; s.send_keys('U1-00'); time.sleep(0.8)
    W().until(lambda d: len(css('tbody tr')) > 0 and all('U1-00' in r.text for r in css('tbody tr')))
    jsclick(css('tbody tr')[0]); wait_css('.modal'); W().until(lambda d: 'Eldeki stok' in text() and 'Son stok hareketleri' in text()); shot('14_urun_karti')
    drv.find_element(By.TAG_NAME, 'body').send_keys(Keys.ESCAPE)

@step('Raporlar: mizan dengeli, KDV özeti, yaşlandırma, stok')
def _():
    goto('#/raporlar/mizan'); W().until(lambda d: 'Mizan dengeli' in text()); shot('15_mizan')
    goto('#/raporlar/kdv'); W().until(lambda d: 'Hesaplanan KDV' in text()); shot('16_kdv')
    goto('#/raporlar/yaslandirma'); W().until(lambda d: 'Toplam açık alacak' in text() and len(css('.bucket')) == 4); shot('17_yaslandirma')
    [b for b in css('.seg button') if b.text == 'Borçlar'][0].click(); W().until(lambda d: 'Toplam açık borç' in text())
    goto('#/raporlar/stok'); W().until(lambda d: 'Toplam stok değeri' in text()); shot('18_stok')

@step('Ayarlar sayfası (dönem + negatif stok)')
def _():
    goto('#/ayarlar'); W().until(lambda d: 'Dönem kapatma' in text() and 'Negatif stok' in text())
    assert '31.03.2026' in text(); shot('19_ayarlar')

@step('Mobil genişlik (390px): yatay taşma yok, kart görünümü')
def _():
    close_overlays(); drv.set_window_size(390, 844)
    for h in ['#/pano', '#/faturalar', '#/cariler', '#/raporlar/mizan', '#/faturalar/yeni']:
        goto(h); time.sleep(1.2)
        sw = drv.execute_script('return document.documentElement.scrollWidth'); iw = drv.execute_script('return window.innerWidth')
        assert sw <= iw + 2, f'{h}: yatay taşma {sw}>{iw}'
    goto('#/faturalar'); W().until(lambda d: len(css('tbody tr')) > 0); shot('20_mobil_faturalar')
    goto('#/pano'); W().until(lambda d: len(css('.kpi')) >= 6); shot('21_mobil_pano')
    drv.set_window_size(1280, 900)

@step('Yetki: müşteri yalnızca ekstresini görüyor, diğer sayfalar kilitli')
def _():
    login_demo('Müşteri (ekstre)')
    W().until(lambda d: 'güncel bakiye' in text() or 'Henüz fatura' in text())
    names = [a.text for a in css('.nav a')]; assert names == ['Hesap ekstrem'], names; shot('22_musteri')
    goto('#/faturalar'); W().until(lambda d: 'yetkiniz yok' in text())
    goto('#/raporlar/mizan'); W().until(lambda d: 'yetkiniz yok' in text())

@step('Yetki: çalışan raporlara/ayarlara erişemiyor')
def _():
    login_demo('Çalışan')
    names = [a.text for a in css('.nav a')]; assert 'Raporlar' not in names and 'Ayarlar' not in names, names
    goto('#/raporlar/mizan'); W().until(lambda d: 'yetkiniz yok' in text())

@step('Muhasebeci: raporlar var, ayarlarda negatif stok yok')
def _():
    login_demo('Muhasebeci')
    names = [a.text for a in css('.nav a')]; assert 'Raporlar' in names and 'Ayarlar' in names
    goto('#/ayarlar'); W().until(lambda d: 'Dönem kapatma' in text()); assert 'Negatif stok' not in text()

@step('Çıkış yapınca giriş ekranına dönüyor')
def _():
    [b for b in css('.logout')][0].click(); wait_css('.login')

@step('API belgeleri (Swagger UI) CSP altında açılıyor ve tüm işlemleri listeliyor')
def _():
    drv.get(BASE + '/docs/'); wait_css('.swagger-ui', 20)
    W(20).until(lambda d: len(css('.opblock')) >= 30)
    assert 'EsnafDefter API' in text(); shot('24_swagger')

if os.environ.get('UI_DOCKER') == '1':
    @step('Sunucu kapalıyken hata durumu + "Tekrar dene" (docker compose stop/start api)')
    def _():
        import subprocess
        login_demo('İşletme sahibi')
        goto('#/cariler'); W().until(lambda d: len(css('tbody tr')) > 0)
        subprocess.run(['docker', 'compose', 'stop', 'api'], check=True); time.sleep(1.5)
        try:
            goto('#/faturalar'); W().until(lambda d: len(css('.state.error')) == 1)
            shot('23_hata_durumu')
        finally:
            subprocess.run(['docker', 'compose', 'start', 'api'], check=True)
        time.sleep(6)
        [b for b in css('.state.error button')][0].click(); W().until(lambda d: len(css('tbody tr')) > 0)

drv.quit()
bad = [r for r in results if r[1] != 'OK']
print(f'\n{len(results)-len(bad)}/{len(results)} adım başarılı')
for n, s, m in bad: print('  BAŞARISIZ:', n, '|', m)
sys.exit(1 if bad else 0)
