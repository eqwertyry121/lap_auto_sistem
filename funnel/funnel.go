// Package funnel — детерминированная воронка L0–L5 (PLAN_v4, Фаза 4).
// Порядок строго «дёшево → дорого»: до Gemini доходят только лоты, не
// отсечённые L0–L2 и не распознанные regex'ом (L3). Вердикт L5 собирается
// из фактов, а не генерится моделью.
package funnel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"kpbot/config"
	"kpbot/filters"
	"kpbot/hw"
	"kpbot/models"
	"kpbot/pricing"
	"kpbot/specs"
	"kpbot/storage"
	"kpbot/vision"
)

// Коды вердиктов (PLAN_v4 §3.6 + PLAN_v5).
const (
	vcDiamond   = "DIAMOND"
	vcSuspect   = "DIAMOND_SUSPECT"
	vcFair      = "FAIR"
	vcExpensive = "EXPENSIVE"
	vcCheck     = "CHECK"
	vcManual    = "MANUAL"
	vcShop      = "SHOP"
	vcJunk      = "JUNK"
	vcSanity    = "SANITY"
	vcNoMarket  = "NO_MARKET"
	vcBanMac    = "BAN_MAC"        // PLAN_v5: запрещённая линейка (MacBook)
	vcNoGpu     = "NO_GPU"         // PLAN_v5: нет дискретной видеокарты
	vcMoose     = "RARE_NO_MARKET" // железо добыто, но в данных KP не с чем сравнить
)

// Funnel — рыночная модель + эталон железа под блокировкой чтения.
type Funnel struct {
	mu     sync.RWMutex
	market *pricing.Market
	cpus   map[string]hw.CPU
	gpus   map[string]hw.GPU
	rate   float64 // RSD→EUR (живой курс, fallback 117.2)
}

func NewFunnel() *Funnel { return &Funnel{rate: pricing.FallbackRsdEur} }

// RefreshMarket перечитывает research.db и hw.db. Вызывается при старте и
// раз в MARKET_REFRESH_MIN; ошибка не рушит старую модель.
// PLAN_v5: при REQUIRE_DGPU=1 рынок собирается только по dGPU-лотам —
// медианы и гейты не смешивают офисные ноутбуки с игровыми.
func (f *Funnel) RefreshMarket(ctx context.Context, cfg *config.Config, log *slog.Logger) {
	opts := pricing.DefaultOptions()
	opts.DGPUOnly = cfg.RequireDGPU
	m, err := pricing.LoadMarket(ctx, cfg.ResearchDBPath, opts)
	if err != nil {
		log.Error("воронка: рынок не обновлён", "err", err)
		return
	}
	cpus, err := hw.LoadCPUs(ctx, "data/hw.db")
	if err != nil {
		log.Error("воронка: эталон CPU", "err", err)
		return
	}
	gpus, err := hw.LoadGPUs(ctx, "data/hw.db")
	if err != nil {
		log.Error("воронка: эталон GPU", "err", err)
		return
	}
	f.mu.Lock()
	f.market = m
	f.cpus = cpus
	f.gpus = gpus
	f.rate = m.RsdEurRate()
	f.mu.Unlock()
	hed := m.Hedonic()
	log.Info("воронка: рынок обновлён",
		"лотов", len(m.Lots()), "пул", len(m.Pool()),
		"hedonic_usable", hed.Usable, "hedonic_n", hed.N,
		"hedonic_r2", fmt.Sprintf("%.3f", hed.R2),
		"курс_rsd", fmt.Sprintf("%.2f", m.RsdEurRate()), "источник_курса", m.RateSource(),
		"dgpu_only", cfg.RequireDGPU)
}

func (f *Funnel) snapshot() (*pricing.Market, map[string]hw.CPU, map[string]hw.GPU, float64) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.market, f.cpus, f.gpus, f.rate
}

// MarketOnly — рынок для пульта/отчётов.
func (f *Funnel) MarketOnly() *pricing.Market {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.market
}

// ---------- чистые решения (тестируются без БД и сети) ----------

// decideBan — запрещённые линейки (PLAN_v5, Фаза A): MacBook и пр. из
// BANNED_MODELS. Чистая функция — проверяется без БД и сети.
func decideBan(titleSnip string, banned []string) (bool, string) {
	return filters.IsBannedModel(titleSnip, banned)
}

// decideL0 — sanity цены (PLAN_v4 §3.1): диапазон + обмен.
func decideL0(priceEUR float64, exchange bool, titleSnip string) (bool, string) {
	if priceEUR < 1 {
		return false, "цена ниже €1"
	}
	if priceEUR > 5000 {
		return false, "цена выше €5000"
	}
	low := strings.ToLower(titleSnip)
	if exchange || strings.Contains(low, "zamena") {
		return false, "обмен (zamena)"
	}
	return true, ""
}

// l5Input — факты для вердикта (без БД/сети — только числа и классы).
type l5Input struct {
	JunkClass   string  // CLEAN / DEFECT / BROKEN_UNCERTAIN
	CPUName     string  // имя CPU (даже без балла); пусто = не определён
	CPUScore    float64 // 0 = CPU вне эталона hw.db
	DevOK       bool
	Dev         float64
	N           int
	Dominated   bool
	YoungSeller bool    // аккаунт < 30 дней
	diamondDev  float64 // отрицательные пороги (например −0.15 / −0.40)
	suspectDev  float64
	marketTol   float64 // «рыночная цена»: отклонение ≤ +marketTol
	minN        int
}

// decideL5 — детерминированный вердикт (PLAN_v6/v7: «лучшие из лучших по низу
// рынка», цены ТОЛЬКО из данных KP). Гейт «есть мощнее за те деньги» УБРАН —
// он отсекал весь низ рынка цепочкой 300→310→320. «Шаг вверх» показывается в
// алерте цифрами и ничего не отклоняет. Некритичный дефект = «нюанс».
func decideL5(in l5Input) string {
	// Сомнительный хлам → ручная проверка. Некритичный дефект (DEFECT) —
	// не блок, а «нюанс» в алерте, поэтому здесь только UNCERTAIN.
	if in.JunkClass == filters.JunkUncertain {
		return vcCheck
	}
	if in.CPUName == "" {
		return vcManual // CPU реально не определён — «нужно посмотреть»
	}
	if !in.DevOK {
		// Железо опознано, но в НАШИХ данных KP нет группы для сравнения
		// (новое/редкое железо). Не молчим — шлём сводку «Владелец лось».
		return vcMoose
	}
	if in.Dev > in.marketTol {
		return vcExpensive // дороже рынка — не интересно
	}
	if in.CPUScore <= 0 {
		return vcCheck // медиана есть, но CPU вне эталона — проверить
	}
	// Цена в низу рынка.
	if in.Dominated && in.Dev <= in.diamondDev {
		return vcCheck
	}
	if in.Dev < in.suspectDev {
		return vcSuspect // слишком дёшево — возможна приманка
	}
	if in.Dev <= in.diamondDev {
		if in.YoungSeller {
			return vcSuspect // глубокая скидка + молодой аккаунт — двойной риск
		}
		if in.N >= in.minN {
			return vcDiamond
		}
		return vcCheck // заметно дешевле, но опорная группа мала
	}
	// Между алмазом и «рынком»: рыночная цена — тихо (перепродаже там нечего делать).
	return vcFair
}

// manualAlertWorthy — слать ли алерт «нужно посмотреть» по нераспознанному
// лоту: дешевле minEUR время на ручные проверки не тратится.
func manualAlertWorthy(priceEUR float64, minEUR int) bool {
	return priceEUR >= float64(minEUR)
}

// mooseAlertWorthy — порог цены для сводки редкого железа (PLAN_v8): дешёвое редкое
// железо без рыночной группы — шум, дешевле minEUR пишем только в аудит.
func mooseAlertWorthy(priceEUR float64, minEUR int) bool {
	return priceEUR >= float64(minEUR)
}

// ---------- полный прогон лота ----------

// Outcome — результат прогона для вызывающего кода.
type Outcome struct {
	Code      string
	Status    models.Status
	AlertText string // HTML для Telegram (пусто — ничего не слать)
	AlertURL  string
	Audit     storage.FunnelVerdict
}

// Run — полный прогон L0–L5 по лоту с уже скачанными деталями.
func Run(ctx context.Context, f *Funnel, cfg *config.Config, gem *vision.GeminiClient,
	log *slog.Logger, ad models.SearchAd, detail *models.AdDetail) Outcome {

	market, cpus, gpus, rate := f.snapshot()
	priceEUR := pricing.ToEUR(float64(ad.Price), models.NormalizeCurrency(ad.Currency), rate)
	bump := func(layer string) { _ = storage.BumpFunnelStat(ctx, cfg.ResearchDBPath, layer) }

	tr := &traceBuf{enabled: cfg.FunnelTrace}
	flush := func() { flushTrace(ad.AdID, tr.lines) }
	tr.f("заголовок: %s", ad.Name)
	tr.f("цена: %s → %.0f€ · продавец: %s (user_id=%d)",
		models.FormatPrice(float64(ad.Price), ad.Currency), priceEUR, detail.Seller(), ad.UserID)
	tr.f("метки KP (используются в L1 как М1/М2): trgovac=%v kp_izlog=%v condition=%q",
		detail.IsTrader(), detail.KPIzlog, detail.Condition)

	// ---- Бан-лист (PLAN_v5, Фаза A): запрещённые линейки (MacBook) ----
	if banned, hit := decideBan(ad.Name+" "+ad.DescriptionSnip, cfg.BannedModels); banned {
		bump("BAN_MAC")
		tr.f("БАН: запрещённая линейка (маркер «%s») → тихий скип", hit)
		flush()
		return silent(vcBanMac, "бан-лист: "+hit)
	}

	// ---- L0: sanity цены (0 запросов) ----
	if pass, why := decideL0(priceEUR, ad.Exchange, ad.Name+" "+ad.DescriptionSnip); !pass {
		bump("L0_" + "REJECT")
		tr.f("L0: ОТКЛОНЁН — %s → итог SANITY (тихо)", why)
		flush()
		return silent(vcSanity, why)
	}
	tr.f("L0: пройден (цена в диапазоне €1–5000, не обмен)")

	// ---- L1: магазины ----
	descPlain := filters.StripHTML(detail.Description)
	seller, serr := storage.LoadSellerInfo(ctx, cfg.ResearchDBPath, ad.UserID)
	if serr != nil {
		log.Warn("воронка: реестр продавцов", "user_id", ad.UserID, "err", serr)
	}
	reviews := int(detail.User.Reviews)
	if seller.Reviews > reviews {
		reviews = seller.Reviews
	}
	facts := filters.AdFacts{
		Title: ad.Name, Description: descPlain, Seller: detail.Seller(),
		Condition: detail.Condition, IsTrader: detail.IsTrader(), KPIzlog: detail.KPIzlog,
		IsRenewed: ad.IsRenewed,
		SellerAds: seller.AdsCount, SellerAgeDays: seller.AgeDays(),
		Reviews: reviews, SellerTraderSeen: seller.TraderSeen,
		SellerKPIzlogSeen: seller.KPIzlogSeen,
	}
	if v := filters.L1(facts); v.Class == filters.ClassShop {
		bump("L1_SHOP")
		tr.f("L1: МАГАЗИН — %s → итог SHOP (тихо)", strings.Join(v.Reasons, "; "))
		flush()
		return silent(vcShop, strings.Join(v.Reasons, "; "))
	}
	tr.f("L1: пройден (нет магазинных признаков: ни меток KP, ни маркеров текста)")

	// ---- L2: хлам ----
	junk := filters.L2(facts)
	if junk.Class == filters.JunkPartsOnly {
		bump("L2_JUNK")
		tr.f("L2: ХЛАМ/запчасти — %s → итог JUNK (тихо)", strings.Join(junk.Reasons, "; "))
		flush()
		return silent(vcJunk, strings.Join(junk.Reasons, "; "))
	}
	if len(junk.Reasons) > 0 {
		tr.f("L2: %s — %s", junk.Class, strings.Join(junk.Reasons, "; "))
	} else {
		tr.f("L2: CLEAN")
	}

	// ---- L3: конфигурация (regex → Gemini-текст → Gemini-фото) ----
	tr.f("L3.1 regex: разбираю текст (заголовок + описание, %d симв.)", len(ad.Name)+len(descPlain))
	recognized := specs.Extract(ad.Name + " " + descPlain)
	cpuModel, cpuScore := matchCPU(cpus, recognized.CPU)
	gpuModel, gpuScore := matchGPU(gpus, recognized.GPU)
	tr.f("L3.1 regex: cpu=%q ram=%d ssd=%d gpu=%q", recognized.CPU, recognized.RAMGB, recognized.SSDGB, recognized.GPU)
	switch {
	case recognized.CPU == "":
		tr.f("L3.1 regex: CPU в тексте не найден")
	case cpuScore > 0:
		tr.f("L3.1 regex: CPU найден в эталоне hw.db: %s (балл %.0f)", cpuModel, cpuScore)
	default:
		tr.f("L3.1 regex: CPU %q распознан, но в эталоне hw.db отсутствует (балла нет)", recognized.CPU)
	}

	// Накопление сведений из Gemini-ступеней: модель ноутбука, причина
	// нераспознанного CPU, явная встройка (ТЗ: фото → максимум информации).
	laptopModel, whyNoCPU := specs.ExtractLaptopModel(ad.Name+" "+descPlain), ""
	if laptopModel != "" {
		tr.f("L3.1 regex: модель ноутбука из текста: %s", laptopModel)
	}
	integratedGPU := false
	via := "regex"
	merge := func(stage, viaName string, gs specs.GeminiSpecs) {
		if gs.LaptopModel != "" && (laptopModel == "" || len(gs.LaptopModel) > len(laptopModel)) {
			laptopModel = gs.LaptopModel
			tr.f("%s: модель ноутбука: %s", stage, laptopModel)
		}
		if m, s := matchCPU(cpus, gs.CPU); s > 0 {
			cpuModel, cpuScore = m, s
			via = viaName
			tr.f("%s: CPU принят: %s (балл %.0f)", stage, cpuModel, cpuScore)
		} else if gs.CPU != "" {
			// CPU назван, но в эталоне hw.db его (ещё) нет: имя сохраняем —
			// лот пойдёт дальше с пометкой «нет балла», а не в MANUAL.
			if cpuModel == "" {
				cpuModel = gs.CPU
				via = viaName
			}
			tr.f("%s: Gemini назвал %q — в эталоне hw.db нет, балл 0 (имя сохранено)", stage, gs.CPU)
		} else {
			tr.f("%s: Gemini не назвал точную модель CPU", stage)
			if gs.WhyNoCPU != "" {
				whyNoCPU = gs.WhyNoCPU
			}
		}
		if gs.GPUIntegrated() {
			integratedGPU = true
		} else if strings.TrimSpace(gs.GPU) != "" {
			// Имя dGPU храним даже если его ещё нет в hw.db (балл 0):
			// присутствие дискретной графики важнее балла (PLAN_v5, Фаза A).
			gm, gs2 := matchGPU(gpus, gs.GPU)
			if gpuModel == "" {
				gpuModel = gm
			}
			if gs2 > gpuScore {
				gpuModel, gpuScore = gm, gs2
			}
		}
		if recognized.RAMGB == 0 {
			recognized.RAMGB = gs.RAMGB
		}
		if recognized.SSDGB == 0 {
			recognized.SSDGB = gs.SSDGB
		}
	}

	// Ступени Gemini вызываются, пока реально не хватает ИМЕНИ CPU или
	// дискретной GPU. CPU, который regex уже назвал, но которого ещё нет в
	// hw.db, не является поводом жечь Gemini: L5 умеет честно отправить такой
	// лот в CHECK/RARE_NO_MARKET без баллов.
	var cachedText, cachedPhoto, cachedSearch bool
	if gs, source, ok := loadCachedGeminiSpecs(ctx, cfg.ResearchDBPath, ad.AdID); ok {
		tr.f("L3.cache: найден research_specs source=%s — использую как начальные спеки", source)
		merge("L3.cache", source, gs)
		cachedText = strings.HasPrefix(source, "gemini-")
		cachedPhoto = strings.HasPrefix(source, "gemini-photo-all")
		cachedSearch = strings.HasPrefix(source, "gemini-search")
	}
	missing := func() bool {
		return cpuModel == "" || (cfg.RequireDGPU && gpuModel == "" && !integratedGPU)
	}
	searchedModelThisRun := false
	researchModelSpecs := func() {
		if !cfg.WebResearch || laptopModel == "" || !missing() || cachedSearch || searchedModelThisRun {
			return
		}
		searchedModelThisRun = true
		bump("L3_INTERNET_SPECS")
		bump("L3_4_INTERNET_SPECS")
		tr.f("L3.4 интернет-спеки (%s): модель %q известна, но CPU/GPU не хватает — ищу в интернете", cfg.GeminiSearchModel, laptopModel)
		gs, prompt, raw, err := vision.ModelSpecsResearch(ctx, gem.WithModel(cfg.GeminiSearchModel), laptopModel, ad.Name, "")
		tr.f("L3.4 запрос (промпт): %s", traceTrunc(prompt, 300))
		if err != nil {
			tr.f("L3.4 ошибка: %v", err)
			log.Warn("воронка: интернет-спеки", "ad_id", ad.AdID, "err", err)
			return
		}
		tr.f("L3.4 ответ Gemini (как пришёл): %s", traceTrunc(raw, 400))
		merge("L3.4", "internet", gs)
		if err := saveCachedGeminiSpecs(ctx, cfg.ResearchDBPath, ad.AdID, "gemini-search", gs, cpus, gpus); err != nil {
			log.Warn("воронка: cache gemini-search specs", "ad_id", ad.AdID, "err", err)
		}
	}

	if missing() && !cachedText {
		tr.f("L3.2 Gemini-текст (%s): regex не дал CPU или GPU — спрашиваю Gemini", cfg.GeminiTextModel)
		gs, prompt, raw, err := geminiTextSpecs(ctx, gem.WithModel(cfg.GeminiTextModel), ad.Name, descPlain, detail.Attributes)
		bump("L3_GEMINI_TEXT")
		bump("L3_2_GEMINI_TEXT")
		tr.f("L3.2 запрос Gemini (промпт): %s", traceTrunc(prompt, 700))
		if err != nil {
			tr.f("L3.2 ошибка Gemini: %v", err)
			log.Warn("воронка: gemini-text", "ad_id", ad.AdID, "err", err)
		} else {
			tr.f("L3.2 ответ Gemini (как пришёл): %s", traceTrunc(raw, 400))
			merge("L3.2", "gemini-text", gs)
			if err := saveCachedGeminiSpecs(ctx, cfg.ResearchDBPath, ad.AdID, "gemini-text", gs, cpus, gpus); err != nil {
				log.Warn("воронка: cache gemini-text specs", "ad_id", ad.AdID, "err", err)
			}
		}
	}
	researchModelSpecs()
	if missing() && len(detail.Photos) > 0 && !cachedPhoto {
		tr.f("L3.3 Gemini-фото (%s): текста не хватило — отправляю все %d фото", cfg.GeminiVisionModel, len(detail.Photos))
		for i, ph := range detail.Photos {
			tr.f("L3.3 фото %d/%d: %s", i+1, len(detail.Photos), ph.BestURL())
		}
		gs, note, raw, err := geminiPhotoSpecs(ctx, gem.WithModel(cfg.GeminiVisionModel), ad.Name, descPlain, detail.Photos)
		bump("L3_GEMINI_PHOTO")
		bump("L3_3_GEMINI_PHOTO")
		tr.f("L3.3 запрос Gemini (текстовая часть): %s", traceTrunc(note, 300))
		if err != nil {
			tr.f("L3.3 ошибка Gemini: %v", err)
			log.Warn("воронка: gemini-photo-all", "ad_id", ad.AdID, "err", err)
		} else {
			tr.f("L3.3 ответ Gemini (как пришёл): %s", traceTrunc(raw, 400))
			merge("L3.3", "gemini-photo-all", gs)
			if err := saveCachedGeminiSpecs(ctx, cfg.ResearchDBPath, ad.AdID, "gemini-photo-all", gs, cpus, gpus); err != nil {
				log.Warn("воронка: cache gemini-photo-all specs", "ad_id", ad.AdID, "err", err)
			}
		}
	} else if missing() && len(detail.Photos) == 0 {
		tr.f("L3.3: фото у лота нет — ступень Gemini-фото пропущена")
	}
	researchModelSpecs()

	// ---- L3.4: интернет «модель→железо» (PLAN_v5, Фаза C). Модель ноутбука
	// известна, но CPU или GPU не определены — ищем спеки модели в сети.
	// Найденное валидируется merge'ом по hw.db (нет балла — не принято).
	// Вызывается через researchModelSpecs(): сначала до Vision, потом после
	// Vision, если фото добавило модель, но всё ещё не дало точный CPU/GPU.

	specsLine := buildSpecsLine(laptopModel, cpuModel, recognized.RAMGB, recognized.SSDGB, gpuModel, integratedGPU)
	tr.f("L3 итог: конфигурация = %s (источник: %s)", specsLine, via)
	if cpuModel != "" && cpuScore <= 0 {
		tr.f("L3: CPU %q назван, но в эталоне hw.db нет балла — лот идёт дальше без CPU-гейтов", cpuModel)
	}

	// 👀 конфигурация не распознана никем — «нужно посмотреть» (ТЗ).
	// ВАЖНО: сюда попадают только лоты, где CPU реально НЕ ОПРЕДЕЛЁН.
	// «CPU назван, но его нет в hw.db» — не сюда, а дальше без CPU-гейтов.
	// Алерт — только от MANUAL_MIN_EUR: дешёвые лоты не стоят времени.
	if cpuScore <= 0 && cpuModel == "" {
		bump("L3_MANUAL")
		reason := strings.TrimSpace(whyNoCPU)
		if reason == "" {
			if laptopModel != "" {
				reason = "модель найдена (" + laptopModel + "), но точный CPU не определён"
			} else {
				reason = "зацепок для определения модели не нашлось"
			}
		}
		if manualAlertWorthy(priceEUR, cfg.ManualMinEUR) {
			bump("MANUAL_ALERT")
			tr.f("ИТОГ: 👀 НУЖНО ПОСМОТРЕТЬ — причина: %s · цена %.0f€ ≥ порога %d€ → алерт в Telegram",
				reason, priceEUR, cfg.ManualMinEUR)
			flush()
			text := manualAlertText(ad, priceEUR, detail.Seller(), reason)
			return Outcome{
				Code: vcManual, Status: models.StatusNeedCheck, AlertText: text, AlertURL: ad.URL(),
				Audit: storage.FunnelVerdict{Code: vcManual, Reason: "L3: конфигурация не распознана — " + reason, Specs: specsLine},
			}
		}
		tr.f("ИТОГ: MANUAL тихо — причина: %s · цена %.0f€ < порога %d€ (время не тратим)",
			reason, priceEUR, cfg.ManualMinEUR)
		flush()
		return Outcome{
			Code: vcManual, Status: models.StatusNoDeal,
			Audit: storage.FunnelVerdict{Code: vcManual,
				Reason: fmt.Sprintf("L3: конфигурация не распознана — %s; цена ниже порога ручных проверок %d€", reason, cfg.ManualMinEUR),
				Specs:  specsLine},
		}
	}

	// ---- dGPU-гейт (PLAN_v5, Фаза A): ноутбуки без дискретной видеокарты
	// не берём вообще — тихий скип, в Telegram не шлём.
	if cfg.RequireDGPU && gpuModel == "" {
		bump("NO_GPU")
		if integratedGPU {
			tr.f("ИТОГ: NO_GPU тихо — графика только встроенная (дискретной нет)")
		} else {
			tr.f("ИТОГ: NO_GPU тихо — дискретная видеокарта не найдена")
		}
		flush()
		return Outcome{
			Code: vcNoGpu, Status: models.StatusNoDeal,
			Audit: storage.FunnelVerdict{Code: vcNoGpu,
				Reason: "дискретной видеокарты нет — лот вне фокуса (PLAN_v5)", Specs: specsLine},
		}
	}

	// ---- L4: цена/качество ----
	if market == nil {
		tr.f("L4: рыночная модель ещё не загружена → итог NO_MARKET (тихо)")
		flush()
		return silent(vcNoMarket, "рыночная модель ещё не загружена")
	}
	lot := pricing.Lot{
		AdID: ad.AdID, Title: ad.Name, URL: ad.URL(), Price: priceEUR,
		Kind: "USED", CPUModel: cpuModel, CPUScore: cpuScore,
		RAMGB: recognized.RAMGB, SSDGB: recognized.SSDGB, GPUModel: gpuModel, GPUScore: gpuScore,
	}
	eval := market.Evaluate(lot)
	est := eval.Estimate
	dev, devOK := eval.Deviation, eval.DevOK
	facts.PriceEUR = priceEUR
	facts.MedianEUR = eval.ComparableMedian
	if lateJunk := filters.L2(facts); lateJunk.Class != junk.Class || strings.Join(lateJunk.Reasons, "; ") != strings.Join(junk.Reasons, "; ") {
		junk = lateJunk
		tr.f("L2 late price-aware: %s — %s", junk.Class, strings.Join(junk.Reasons, "; "))
	}
	if junk.Class == filters.JunkPartsOnly {
		bump("L2_JUNK_LATE")
		tr.f("L2 late: ХЛАМ/запчасти после price-aware cross-check → итог JUNK (тихо)")
		flush()
		return silent(vcJunk, strings.Join(junk.Reasons, "; "))
	}
	if devOK {
		tr.f("L4: медиана конфигурации %.0f€ (n=%d, уровень %s) · цена лота %.0f€ · отклонение %+.0f%%",
			est.Median, est.N, est.Level, priceEUR, dev*100)
	} else {
		tr.f("L4: рыночной группы для конфигурации нет (мало данных KP) — сравнить не с чем")
	}

	// ---- L5: вердикт (PLAN_v6) ----
	// ВАЖНО: цены берём ТОЛЬКО из наших данных KP (медианы dGPU-пула).
	// Никаких внешних цен через Gemini — интернет используется только чтобы
	// узнать, ЧТО за ноутбук (L3.4 модель→железо), а не сколько он стоит.
	// «Шаг вверх» — ближайший смысловой апгрейд: мощнее в целом (CPU+GPU) И
	// дороже, с минимальной ценой за прирост мощности. Показывается в алерте
	// цифрами и НИЧЕГО не отклоняет (гейт «мощнее за те же деньги» убран —
	// он отсекал весь низ рынка цепочкой 300→310→320).
	stepUp := eval.StepUp
	if stepUp != nil && stepUp.URL != "" {
		tr.f("L5 шаг вверх: %q €%.0f (+€%.0f, +%.0f баллов) %s", stepUp.Title, stepUp.Price,
			stepUp.Price-lot.Price, stepUp.Composite()-lot.Composite(), stepUp.URL)
	} else {
		tr.f("L5 шаг вверх: мощнее и дороже на рынке не найдено")
	}

	// Некритичный дефект не блокирует — «хороший, но с нюансом».
	nuance := ""
	if junk.Class == filters.JunkDefect {
		nuance = strings.Join(junk.Reasons, "; ")
	}

	code := decideL5(l5Input{
		JunkClass: junk.Class, CPUName: cpuModel, CPUScore: cpuScore, DevOK: devOK, Dev: dev, N: est.N,
		Dominated:   eval.DominatedBy != nil,
		YoungSeller: seller.AgeDays() >= 0 && seller.AgeDays() < 30,
		diamondDev:  float64(cfg.DiamondDevPct) / 100,
		suspectDev:  float64(cfg.SuspectDevPct) / 100,
		marketTol:   float64(cfg.MarketTolPct) / 100,
		minN:        cfg.DiamondMinN,
	})
	tr.f("L5: вердикт %s (рынок ≤ +%d%%, алмаз ≤ %d%%, подозрение < %d%%, мин. n=%d)",
		code, cfg.MarketTolPct, cfg.DiamondDevPct, cfg.SuspectDevPct, cfg.DiamondMinN)

	alts := collectAlternatives(market, lot)
	altsJSON, _ := json.Marshal(alts)
	marketRef := "в данных KP нет группы для сравнения"
	if devOK {
		marketRef = fmt.Sprintf("ориентир=€%.0f (n=%d, %s)", est.Median, est.N, est.Level)
		if est.Capped() && est.CapLot != nil {
			marketRef = fmt.Sprintf("ориентир=€%.0f, сырой=€%.0f, потолок по более мощному лоту %d",
				est.Median, est.RawMedian, est.CapLot.AdID)
		}
	}
	if devOK {
		marketRef = fmt.Sprintf("comparable_median=€%.0f p25=€%.0f (n=%d, %s, confidence=%s)",
			eval.ComparableMedian, eval.ComparableP25, est.N, est.Level, eval.Confidence)
		ceilingBy := eval.OpportunityBy
		if ceilingBy == nil {
			ceilingBy = eval.DominatedBy
		}
		if eval.OpportunityCeiling > 0 && ceilingBy != nil {
			marketRef += fmt.Sprintf("; opportunity_ceiling=€%.0f by stronger lot %d %s",
				eval.OpportunityCeiling, ceilingBy.AdID, ceilingBy.URL)
		}
	}
	audit := storage.FunnelVerdict{
		Code: code, Deviation: dev, GroupN: est.N, Alternatives: string(altsJSON),
		Reason: fmt.Sprintf("L3=%s; L2=%s; %s", via, junk.Class, marketRef),
		Specs:  specsLine,
	}

	specsScore := specsScoreLine(laptopModel, cpuModel, cpuScore, recognized.RAMGB, recognized.SSDGB, gpuModel, gpuScore, integratedGPU)

	switch code {
	case vcDiamond, vcSuspect:
		bump(code)
		tr.f("ИТОГ: %s — АЛЕРТ в Telegram", code)
		flush()
		text := valueAlertText(code, ad, specsScore, lot, est, dev, nuance, stepUp, eval)
		return Outcome{Code: code, Status: models.StatusAlerted, AlertText: text, AlertURL: ad.URL(), Audit: audit}
	case vcMoose:
		bump(code)
		if !mooseAlertWorthy(priceEUR, cfg.MooseMinEUR) {
			tr.f("ИТОГ: RARE_NO_MARKET тихо — сравнить не с чем · цена %.0f€ < порога %d€ (время не тратим)",
				priceEUR, cfg.MooseMinEUR)
			flush()
			return Outcome{Code: code, Status: models.StatusNoDeal, Audit: audit}
		}
		tr.f("ИТОГ: RARE_NO_MARKET — редкое железо, сравнить не с чем → сводка в Telegram")
		flush()
		text := mooseAlertText(ad, specsScore, lot, nuance)
		return Outcome{Code: code, Status: models.StatusNeedCheck, AlertText: text, AlertURL: ad.URL(), Audit: audit}
	case vcCheck:
		bump("CHECK")
		tr.f("ИТОГ: CHECK — алерт «проверка» в Telegram")
		flush()
		text := checkAlertText(ad, priceEUR, specsScore, est, devOK, dev, junk.Reasons, cpuScore <= 0, nuance, eval)
		return Outcome{Code: code, Status: models.StatusNeedCheck, AlertText: text, AlertURL: ad.URL(), Audit: audit}
	default:
		bump(code)
		tr.f("ИТОГ: %s — тихо (только запись в БД)", code)
		flush()
		return Outcome{Code: code, Status: models.StatusNoDeal, Audit: audit}
	}
}

// silent — тихий срез (только аудит в БД, без Telegram).
func silent(code, reason string) Outcome {
	return Outcome{
		Code: code, Status: models.StatusNoDeal,
		Audit: storage.FunnelVerdict{Code: code, Reason: reason},
	}
}

// ---------- трассировка воронки (FUNNEL_TRACE) ----------
//
// Человекочитаемый журнал data/funnel_trace.log: по каждому лоту видно ВСЕ
// шаги L0–L5, включая точные запросы к Gemini и ответы на них. Смотреть:
//
//	Get-Content data\funnel_trace.log -Wait -Tail 40 -Encoding UTF8

const tracePath = "data/funnel_trace.log"

var traceMu sync.Mutex

// traceBuf — накапливает строки трассировки одного лота.
type traceBuf struct {
	enabled bool
	lines   []string
}

func (t *traceBuf) f(format string, args ...any) {
	if t.enabled {
		t.lines = append(t.lines, fmt.Sprintf(format, args...))
	}
}

// flushTrace пишет блок лота в журнал целиком (один лот = один блок).
// При разрастании файла — ротация в .1.
func flushTrace(adID int64, lines []string) {
	if len(lines) == 0 {
		return
	}
	traceMu.Lock()
	defer traceMu.Unlock()

	if fi, err := os.Stat(tracePath); err == nil && fi.Size() > 10<<20 {
		_ = os.Rename(tracePath, tracePath+".1")
	}
	f, err := os.OpenFile(tracePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "════ %s · лот %d ════\n", time.Now().Format("2006-01-02 15:04:05"), adID)
	for _, ln := range lines {
		fmt.Fprintln(f, "  "+ln)
	}
}

func traceTrunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return truncateRunes(s, n)
}

func matchCPU(cpus map[string]hw.CPU, name string) (string, float64) {
	if name == "" || cpus == nil {
		return name, 0
	}
	if c, ok := cpus[hw.Key(name)]; ok {
		return c.Name, c.Score
	}
	return name, 0
}

func matchGPU(gpus map[string]hw.GPU, name string) (string, float64) {
	if name == "" || gpus == nil {
		return name, 0
	}
	if g, ok := gpus[hw.Key(name)]; ok {
		return g.Name, g.Score
	}
	return name, 0
}

func buildSpecsLine(laptop, cpu string, ram, ssd int, gpu string, integratedGPU bool) string {
	parts := []string{}
	if laptop != "" {
		parts = append(parts, laptop)
	}
	if cpu != "" {
		parts = append(parts, cpu)
	}
	if ram > 0 {
		parts = append(parts, fmt.Sprintf("%dGB", ram))
	}
	if ssd > 0 {
		parts = append(parts, fmt.Sprintf("SSD %dGB", ssd))
	}
	if gpu != "" {
		parts = append(parts, gpu)
	} else if integratedGPU {
		parts = append(parts, "встроенная графика")
	}
	if len(parts) == 0 {
		return "железо не распознано"
	}
	return strings.Join(parts, " · ")
}

// ---------- Gemini-ступени Л3 ----------
// Каждая ступень возвращает (спеки, отправленный запрос, сырой ответ, ошибка) —
// всё это попадает в трассировку, чтобы любой вызов можно было проследить.

func loadCachedGeminiSpecs(ctx context.Context, dbPath string, adID int64) (specs.GeminiSpecs, string, bool) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return specs.GeminiSpecs{}, "", false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, _ = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`)
	var gs specs.GeminiSpecs
	var source string
	err = db.QueryRowContext(ctx, `
SELECT COALESCE(cpu_model,''), COALESCE(ram_gb,0), COALESCE(ssd_gb,0), COALESCE(gpu_model,''), COALESCE(source,'')
FROM research_specs
WHERE ad_id=? AND source LIKE 'gemini%'
LIMIT 1`, adID).Scan(&gs.CPU, &gs.RAMGB, &gs.SSDGB, &gs.GPU, &source)
	if err != nil {
		return specs.GeminiSpecs{}, "", false
	}
	return gs, source, true
}

func saveCachedGeminiSpecs(ctx context.Context, dbPath string, adID int64, source string, gs specs.GeminiSpecs,
	cpus map[string]hw.CPU, gpus map[string]hw.GPU) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, _ = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`)

	cpuModel, cpuScore := matchCPU(cpus, strings.TrimSpace(gs.CPU))
	gpuModel, gpuScore := matchGPU(gpus, strings.TrimSpace(gs.GPU))
	_, err = db.ExecContext(ctx, `
INSERT INTO research_specs (ad_id, cpu_model, cpu_score, ram_gb, ssd_gb, gpu_model, gpu_score, updated_at, source)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(ad_id) DO UPDATE SET
	cpu_model=excluded.cpu_model, cpu_score=excluded.cpu_score,
	ram_gb=excluded.ram_gb, ssd_gb=excluded.ssd_gb,
	gpu_model=excluded.gpu_model, gpu_score=excluded.gpu_score,
	updated_at=excluded.updated_at, source=excluded.source`,
		adID, cpuModel, cpuScore, gs.RAMGB, gs.SSDGB, gpuModel, gpuScore, time.Now().Unix(), source)
	return err
}

func geminiTextSpecs(ctx context.Context, gem *vision.GeminiClient, title, descPlain string, attrs []models.Attribute) (specs.GeminiSpecs, string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Заголовок: %s\n", title)
	fmt.Fprintf(&b, "Описание: %s\n", truncateRunes(descPlain, 2500))
	if len(attrs) > 0 {
		b.WriteString("Характеристики из объявления:\n")
		for _, a := range attrs {
			if a.Name == "" && a.Value == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", a.Name, a.Value)
		}
	}
	prompt := b.String()
	out, err := gem.Generate(ctx, specs.GeminiSpecsPrompt, []vision.Part{{Text: prompt}})
	if err != nil {
		return specs.GeminiSpecs{}, prompt, "", err
	}
	gs, perr := specs.ParseGeminiSpecs(out)
	return gs, prompt, out, perr
}

func geminiPhotoSpecs(ctx context.Context, gem *vision.GeminiClient, title, descPlain string, photos []models.PhotoDoc) (specs.GeminiSpecs, string, string, error) {
	note := fmt.Sprintf(
		"Заголовок: %s\nОписание: %s\n\nМодель не удалось определить по тексту. Внимательно рассмотри наклейки процессора, шильдики на дне, гравировки модели и скриншоты характеристик, которые часто лежат в конце галереи.",
		title, truncateRunes(descPlain, 800))
	parts := []vision.Part{{Text: note}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ph := range photos {
		u := ph.BestURL()
		if u == "" {
			continue
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			p, err := gem.ImageToPart(ctx, u)
			if err != nil {
				return
			}
			mu.Lock()
			parts = append(parts, *p)
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	if len(parts) == 1 {
		return specs.GeminiSpecs{}, note, "", fmt.Errorf("фото не скачались")
	}
	out, err := gem.Generate(ctx, specs.GeminiSpecsPrompt, parts)
	if err != nil {
		return specs.GeminiSpecs{}, note, "", err
	}
	gs, perr := specs.ParseGeminiSpecs(out)
	return gs, note, out, perr
}

// ---------- альтернативы и тексты алертов ----------

type altItem struct {
	Kind  string `json:"kind"` // cheaper / stronger / value
	Title string `json:"title"`
	Price int64  `json:"price_eur"`
	URL   string `json:"url"`
}

func collectAlternatives(m *pricing.Market, lot pricing.Lot) []altItem {
	var out []altItem
	for _, l := range m.CheaperSameCPU(lot, 60, 2) {
		out = append(out, altItem{"cheaper", l.Title, int64(l.Price), l.URL})
	}
	for _, l := range m.StrongerForBudget(lot, 60, 2, 1.1) {
		out = append(out, altItem{"stronger", l.Title, int64(l.Price), l.URL})
	}
	return out
}

// specsScoreLine — строка конфигурации с бенчмарк-баллами для алерта-досье
// (PLAN_v5): пользователю видна мощность CPU и GPU, а не только названия.
func specsScoreLine(laptop, cpu string, cpuScore float64, ram, ssd int, gpu string, gpuScore float64, integratedGPU bool) string {
	parts := []string{}
	if laptop != "" {
		parts = append(parts, laptop)
	}
	if cpu != "" {
		if cpuScore > 0 {
			parts = append(parts, fmt.Sprintf("%s (%.0f)", cpu, cpuScore))
		} else {
			parts = append(parts, cpu+" (нет балла в эталоне)")
		}
	}
	if ram > 0 {
		parts = append(parts, fmt.Sprintf("%dGB", ram))
	}
	if ssd > 0 {
		parts = append(parts, fmt.Sprintf("SSD %dGB", ssd))
	}
	if gpu != "" {
		if gpuScore > 0 {
			parts = append(parts, fmt.Sprintf("%s (%.0f)", gpu, gpuScore))
		} else {
			parts = append(parts, gpu+" (нет балла в эталоне)")
		}
	} else if integratedGPU {
		parts = append(parts, "встроенная графика")
	}
	if len(parts) == 0 {
		return "железо не распознано"
	}
	return strings.Join(parts, " · ")
}

// valueAlertText — алерт-досье кандидата в низу рынка (PLAN_v6):
// 💎 DIAMOND / 💎❓ SUSPECT. Только цифры, без прилагательных: средняя цена
// (НАШИ данные KP), баллы кандидата, «шаг вверх» (ближайший мощнее и дороже)
// и сколько он стоит за единицу мощности.
func valueAlertText(code string, ad models.SearchAd, specsScore string, lot pricing.Lot,
	est pricing.PriceEstimate, dev float64, nuance string, stepUp *pricing.Lot, eval pricing.MarketEvaluation) string {
	var b strings.Builder
	switch code {
	case vcSuspect:
		b.WriteString("💎❓ <b>ПОДОЗРИТЕЛЬНО ДЁШЕВО</b>\n\n")
	default:
		b.WriteString("💎 <b>АЛМАЗ — низ рынка</b>\n\n")
	}
	fmt.Fprintf(&b, "<b>%s</b>\n", html.EscapeString(truncateRunes(ad.Name, 90)))
	fmt.Fprintf(&b, "Железо: %s\n", html.EscapeString(specsScore))
	fmt.Fprintf(&b, "Мощность: %.0f баллов · %.0f баллов/€1000\n", lot.Composite(), lot.ValuePer1000())
	fmt.Fprintf(&b, "Цена: <b>€%.0f</b>\n", lot.Price)
	if est.Capped() && est.CapLot != nil && est.CapLot.URL != "" {
		fmt.Fprintf(&b, "Рыночный ориентир: €%.0f (сырая медиана €%.0f, n=%d; потолок по более мощному лоту)\n",
			est.Median, est.RawMedian, est.N)
		fmt.Fprintf(&b, "Контраргумент: %s · €%.0f · %.0f баллов\n",
			html.EscapeString(truncateRunes(est.CapLot.Title, 80)), est.CapLot.Price, est.CapLot.Composite())
		fmt.Fprintf(&b, "↪ %s\n", html.EscapeString(est.CapLot.URL))
		fmt.Fprintf(&b, "Отклонение: <b>%.0f%%</b>\n", dev*100)
	} else {
		fmt.Fprintf(&b, "Рыночный ориентир (наши данные KP): €%.0f (n=%d) · отклонение <b>%.0f%%</b>\n",
			est.Median, est.N, dev*100)
	}
	ceilingBy := eval.OpportunityBy
	if ceilingBy == nil {
		ceilingBy = eval.DominatedBy
	}
	if eval.OpportunityCeiling > 0 && ceilingBy != nil && ceilingBy.URL != "" {
		fmt.Fprintf(&b, "Opportunity ceiling: €%.0f by stronger lot %s · €%.0f · %.0f points\n",
			eval.OpportunityCeiling, html.EscapeString(truncateRunes(ceilingBy.Title, 80)), ceilingBy.Price, ceilingBy.Composite())
		fmt.Fprintf(&b, "↪ %s\n", html.EscapeString(ceilingBy.URL))
	}
	if nuance != "" {
		fmt.Fprintf(&b, "✅ Хороший, но с нюансом: %s\n", html.EscapeString(nuance))
	}
	// Шаг вверх: ближайший мощнее И дороже — цифрами (цена за прирост мощности).
	if stepUp != nil && stepUp.URL != "" {
		dComp := stepUp.Composite() - lot.Composite()
		dPrice := stepUp.Price - lot.Price
		costPer1000 := dPrice / dComp * 1000
		fmt.Fprintf(&b, "\nШаг вверх: %s\n", html.EscapeString(truncateRunes(stepUp.Title, 80)))
		fmt.Fprintf(&b, "· €%.0f (+€%.0f) · %.0f баллов (+%.0f, +%.0f%%)\n",
			stepUp.Price, dPrice, stepUp.Composite(), dComp, dComp/lot.Composite()*100)
		fmt.Fprintf(&b, "· €%.0f за +1000 баллов (у этого лота %.0f баллов/€1000 против %.0f у шага)\n",
			costPer1000, lot.ValuePer1000(), stepUp.ValuePer1000())
		if stepUp.URL != "" {
			fmt.Fprintf(&b, "↪ %s\n", html.EscapeString(stepUp.URL))
		}
	} else {
		fmt.Fprintf(&b, "\nМощнее и дороже на рынке нет: %.0f баллов — максимум за свои деньги.\n", lot.Composite())
	}
	if code == vcSuspect {
		b.WriteString("\n⚠️ Слишком дёшево — проверь продавца (возможна приманка).\n")
	}
	fmt.Fprintf(&b, "\n🔗 %s", html.EscapeString(ad.URL()))
	return b.String()
}

// mooseAlertText — «Редкое железо — сравнить не с чем» (PLAN_v8). Железо
// опознано (часто ПРЯМО из объявления), но в наших данных KP нет группы для
// сравнения цены. Текст не делает заявлений о продавце: формулировка v7
// «владелец лось, но я добыл инфу» клеветала на продавцов, у которых все
// характеристики были указаны в самом лоте (кейсы дня 2026-08-06).
func mooseAlertText(ad models.SearchAd, specsScore string, lot pricing.Lot, nuance string) string {
	var b strings.Builder
	b.WriteString("🦌 <b>РЕДКОЕ ЖЕЛЕЗО — СРАВНИТЬ НЕ С ЧЕМ</b>\n\n")
	fmt.Fprintf(&b, "<b>%s</b>\n", html.EscapeString(truncateRunes(ad.Name, 90)))
	fmt.Fprintf(&b, "Железо: %s\n", html.EscapeString(specsScore))
	if lot.Composite() > 0 {
		fmt.Fprintf(&b, "Мощность: %.0f баллов · %.0f баллов/€1000\n", lot.Composite(), lot.ValuePer1000())
	}
	fmt.Fprintf(&b, "Цена: <b>€%.0f</b>\n", lot.Price)
	b.WriteString("ℹ️ В наших данных KP нет похожих лотов — оценить цену не с чем. Реши по цифрам выше.\n")
	if nuance != "" {
		fmt.Fprintf(&b, "Нюанс: %s\n", html.EscapeString(nuance))
	}
	fmt.Fprintf(&b, "\n🔗 %s", html.EscapeString(ad.URL()))
	return b.String()
}

func checkAlertText(ad models.SearchAd, priceEUR float64, specsScore string,
	est pricing.PriceEstimate, devOK bool, dev float64, junkReasons []string,
	cpuNoScore bool, nuance string, eval pricing.MarketEvaluation) string {
	var b strings.Builder
	b.WriteString("⚠️ <b>ТРЕБУЕТСЯ ПРОВЕРКА</b>\n\n")
	fmt.Fprintf(&b, "<b>%s</b>\n", html.EscapeString(truncateRunes(ad.Name, 90)))
	fmt.Fprintf(&b, "Железо: %s\n", html.EscapeString(specsScore))
	fmt.Fprintf(&b, "Цена: €%.0f\n", priceEUR)
	ceilingBy := eval.OpportunityBy
	if ceilingBy == nil {
		ceilingBy = eval.DominatedBy
	}
	if eval.OpportunityCeiling > 0 && ceilingBy != nil && ceilingBy.URL != "" {
		fmt.Fprintf(&b, "Opportunity ceiling: €%.0f by stronger lot %s · €%.0f · %.0f points\n",
			eval.OpportunityCeiling, html.EscapeString(truncateRunes(ceilingBy.Title, 80)), ceilingBy.Price, ceilingBy.Composite())
		fmt.Fprintf(&b, "↪ %s\n", html.EscapeString(ceilingBy.URL))
	}
	if cpuNoScore {
		b.WriteString("ℹ️ CPU нет в эталоне мощности: сверь поколение сам.\n")
	}
	if devOK {
		if est.Capped() && est.CapLot != nil && est.CapLot.URL != "" {
			fmt.Fprintf(&b, "Рыночный ориентир: €%.0f (сырая медиана €%.0f, n=%d; потолок по более мощному лоту), отклонение %.0f%%\n",
				est.Median, est.RawMedian, est.N, dev*100)
			fmt.Fprintf(&b, "Контраргумент: %s\n", html.EscapeString(est.CapLot.URL))
		} else {
			fmt.Fprintf(&b, "Рыночный ориентир (наши данные KP): €%.0f (n=%d), отклонение %.0f%%\n", est.Median, est.N, dev*100)
		}
	}
	if nuance != "" {
		fmt.Fprintf(&b, "Нюанс: %s\n", html.EscapeString(nuance))
	} else if len(junkReasons) > 0 {
		fmt.Fprintf(&b, "Дефекты: %s\n", html.EscapeString(strings.Join(junkReasons, "; ")))
	}
	fmt.Fprintf(&b, "\n🔗 %s", html.EscapeString(ad.URL()))
	return b.String()
}

// manualAlertText — «нужно посмотреть»: конфигурация не распознана, лот
// дороже MANUAL_MIN_EUR, причину Gemini пишем прямо в алерт.
func manualAlertText(ad models.SearchAd, priceEUR float64, seller, reason string) string {
	var b strings.Builder
	b.WriteString("👀 <b>НУЖНО ПОСМОТРЕТЬ</b> — конфигурация не распознана\n\n")
	fmt.Fprintf(&b, "<b>%s</b>\n", html.EscapeString(truncateRunes(ad.Name, 90)))
	fmt.Fprintf(&b, "Цена: <b>€%.0f</b> · Продавец: %s\n", priceEUR, html.EscapeString(seller))
	fmt.Fprintf(&b, "Почему не распознана: %s\n", html.EscapeString(reason))
	fmt.Fprintf(&b, "\n🔗 %s", html.EscapeString(ad.URL()))
	return b.String()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
