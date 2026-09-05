package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogdomain "github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// ICD-10 as a code system (WP-I5-05 section 2.3).
//
// This is not the ICD-10 list. Importing the whole of it is a licence question the
// customer answers (v1.2 36.2), and the day they answer it the rows arrive through the
// ordinary code value import. What is seeded here is the *shape* the answer will land in:
// the twenty-two chapters as the hierarchy, one intermediate block, and enough real codes
// under them for the demo and the tests to be about something.
//
// The one thing here that is load-bearing rather than illustrative is `sensitive`. WP-I5-01
// derives a health case's sensitivity from `catalog.code_value.attributes->>'sensitive'`
// and stores the answer on the diagnosis, which is what lets a sponsor's HR user be told a
// case exists without being told what it is about. Which chapters those are is a property
// of the code system and is stated here, once — chapter V (mental and behavioural), XV
// (pregnancy and childbirth), XVII (congenital malformations), and the Z30–Z39 block of
// chapter XXI (contraception, procreation, pregnancy supervision). A second place to say
// the same thing would be a second place for it to be wrong.
const (
	icd10Code    = "ICD10"
	icd10Version = "2019"
	icd10Name    = "ICD-10 (Uluslararası Hastalık Sınıflandırması)"
)

// icd10ValidFrom is the day the seeded edition is valid from. It is deliberately far
// enough back that every demo service date falls inside it.
var icd10ValidFrom = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// icd10Row is one seeded value: a chapter, a block or a code.
type icd10Row struct {
	Code      string
	Display   string
	Parent    string
	Sensitive bool
}

// icd10Chapters are the twenty-two chapters, by their code range, which is how every
// publisher of ICD-10 names them.
func icd10Chapters() []icd10Row {
	return []icd10Row{
		{Code: "A00-B99", Display: "I. Bazı enfeksiyöz ve parazit hastalıkları"},
		{Code: "C00-D48", Display: "II. Neoplazmlar"},
		{Code: "D50-D89", Display: "III. Kan ve kan yapıcı organ hastalıkları ve bağışıklık bozuklukları"},
		{Code: "E00-E90", Display: "IV. Endokrin, beslenme ve metabolizma hastalıkları"},
		{Code: "F00-F99", Display: "V. Ruhsal ve davranışsal bozukluklar", Sensitive: true},
		{Code: "G00-G99", Display: "VI. Sinir sistemi hastalıkları"},
		{Code: "H00-H59", Display: "VII. Göz ve adneks hastalıkları"},
		{Code: "H60-H95", Display: "VIII. Kulak ve mastoid çıkıntı hastalıkları"},
		{Code: "I00-I99", Display: "IX. Dolaşım sistemi hastalıkları"},
		{Code: "J00-J99", Display: "X. Solunum sistemi hastalıkları"},
		{Code: "K00-K93", Display: "XI. Sindirim sistemi hastalıkları"},
		{Code: "L00-L99", Display: "XII. Deri ve deri altı doku hastalıkları"},
		{Code: "M00-M99", Display: "XIII. Kas iskelet sistemi ve bağ dokusu hastalıkları"},
		{Code: "N00-N99", Display: "XIV. Genitoüriner sistem hastalıkları"},
		{Code: "O00-O99", Display: "XV. Gebelik, doğum ve lohusalık", Sensitive: true},
		{Code: "P00-P96", Display: "XVI. Perinatal dönemde ortaya çıkan durumlar"},
		{Code: "Q00-Q99", Display: "XVII. Konjenital malformasyonlar ve kromozom anomalileri", Sensitive: true},
		{Code: "R00-R99", Display: "XVIII. Başka yerde sınıflanmamış belirti ve bulgular"},
		{Code: "S00-T98", Display: "XIX. Yaralanma, zehirlenme ve dış nedenlerin sonuçları"},
		{Code: "V01-Y98", Display: "XX. Hastalık ve ölümün dış nedenleri"},
		{Code: "Z00-Z99", Display: "XXI. Sağlık durumunu etkileyen faktörler ve sağlık hizmeti kullanımı"},
		{Code: "U00-U99", Display: "XXII. Özel amaçlı kodlar"},
	}
}

// icd10Codes are the leaves, plus the one intermediate block the sensitivity rule needs.
func icd10Codes() []icd10Row {
	return []icd10Row{
		{Code: "A09", Display: "Enfeksiyöz kökenli olduğu düşünülen diyare ve gastroenterit", Parent: "A00-B99"},
		{Code: "B34.9", Display: "Viral enfeksiyon, tanımlanmamış", Parent: "A00-B99"},

		{Code: "C50.9", Display: "Meme malign neoplazmı, tanımlanmamış", Parent: "C00-D48"},
		{Code: "D12.6", Display: "Kolon benign neoplazmı, tanımlanmamış", Parent: "C00-D48"},

		{Code: "D50.9", Display: "Demir eksikliği anemisi, tanımlanmamış", Parent: "D50-D89"},

		{Code: "E03.9", Display: "Hipotiroidi, tanımlanmamış", Parent: "E00-E90"},
		{Code: "E11.9", Display: "Tip 2 diabetes mellitus, komplikasyonsuz", Parent: "E00-E90"},
		{Code: "E78.5", Display: "Hiperlipidemi, tanımlanmamış", Parent: "E00-E90"},
		{Code: "E66.9", Display: "Obezite, tanımlanmamış", Parent: "E00-E90"},

		// Chapter V: every code under it inherits the chapter's own sensitivity, and each
		// row says so itself so that a reader of one row never has to walk up the tree.
		{Code: "F10.2", Display: "Alkol kullanımına bağlı bağımlılık sendromu", Parent: "F00-F99", Sensitive: true},
		{Code: "F20.0", Display: "Paranoid şizofreni", Parent: "F00-F99", Sensitive: true},
		{Code: "F31.9", Display: "Bipolar duygudurum bozukluğu, tanımlanmamış", Parent: "F00-F99", Sensitive: true},
		{Code: "F32.1", Display: "Orta düzeyde depresif nöbet", Parent: "F00-F99", Sensitive: true},
		{Code: "F41.1", Display: "Yaygın anksiyete bozukluğu", Parent: "F00-F99", Sensitive: true},
		{Code: "F90.0", Display: "Aktivite ve dikkat bozukluğu", Parent: "F00-F99", Sensitive: true},

		{Code: "G43.9", Display: "Migren, tanımlanmamış", Parent: "G00-G99"},
		{Code: "G47.3", Display: "Uyku apnesi", Parent: "G00-G99"},

		{Code: "H10.9", Display: "Konjonktivit, tanımlanmamış", Parent: "H00-H59"},
		{Code: "H52.1", Display: "Miyopi", Parent: "H00-H59"},

		{Code: "H66.9", Display: "Otitis media, tanımlanmamış", Parent: "H60-H95"},

		{Code: "I10", Display: "Esansiyel (primer) hipertansiyon", Parent: "I00-I99"},
		{Code: "I25.1", Display: "Aterosklerotik kalp hastalığı", Parent: "I00-I99"},
		{Code: "I48.9", Display: "Atriyal fibrilasyon ve flutter, tanımlanmamış", Parent: "I00-I99"},

		{Code: "J00", Display: "Akut nazofarenjit (nezle)", Parent: "J00-J99"},
		{Code: "J06.9", Display: "Akut üst solunum yolu enfeksiyonu, tanımlanmamış", Parent: "J00-J99"},
		{Code: "J18.9", Display: "Pnömoni, tanımlanmamış", Parent: "J00-J99"},
		{Code: "J45.9", Display: "Astım, tanımlanmamış", Parent: "J00-J99"},

		{Code: "K02.9", Display: "Diş çürüğü, tanımlanmamış", Parent: "K00-K93"},
		{Code: "K21.9", Display: "Gastroözofageal reflü hastalığı, özofajitsiz", Parent: "K00-K93"},
		{Code: "K29.7", Display: "Gastrit, tanımlanmamış", Parent: "K00-K93"},

		{Code: "L20.9", Display: "Atopik dermatit, tanımlanmamış", Parent: "L00-L99"},
		{Code: "L30.9", Display: "Dermatit, tanımlanmamış", Parent: "L00-L99"},

		{Code: "M17.9", Display: "Gonartroz, tanımlanmamış", Parent: "M00-M99"},
		{Code: "M54.5", Display: "Bel ağrısı", Parent: "M00-M99"},
		{Code: "M75.1", Display: "Rotator manşet sendromu", Parent: "M00-M99"},
		{Code: "M79.7", Display: "Fibromiyalji", Parent: "M00-M99"},

		{Code: "N18.9", Display: "Kronik böbrek hastalığı, tanımlanmamış", Parent: "N00-N99"},
		{Code: "N39.0", Display: "İdrar yolu enfeksiyonu, yeri tanımlanmamış", Parent: "N00-N99"},

		{Code: "O21.0", Display: "Hafif hiperemezis gravidarum", Parent: "O00-O99", Sensitive: true},
		{Code: "O80", Display: "Tek, kendiliğinden doğum", Parent: "O00-O99", Sensitive: true},

		{Code: "P59.9", Display: "Yenidoğan sarılığı, tanımlanmamış", Parent: "P00-P96"},

		{Code: "Q21.0", Display: "Ventriküler septal defekt", Parent: "Q00-Q99", Sensitive: true},
		{Code: "Q90.9", Display: "Down sendromu, tanımlanmamış", Parent: "Q00-Q99", Sensitive: true},

		{Code: "R05", Display: "Öksürük", Parent: "R00-R99"},
		{Code: "R10.4", Display: "Diğer ve tanımlanmamış karın ağrısı", Parent: "R00-R99"},
		{Code: "R51", Display: "Baş ağrısı", Parent: "R00-R99"},

		{Code: "S52.5", Display: "Radius alt uç kırığı", Parent: "S00-T98"},
		{Code: "S93.4", Display: "Ayak bileği burkulması ve zorlanması", Parent: "S00-T98"},

		{Code: "W19", Display: "Tanımlanmamış düşme", Parent: "V01-Y98"},

		{Code: "Z00.0", Display: "Genel tıbbi muayene", Parent: "Z00-Z99"},
		{Code: "Z01.0", Display: "Göz ve görme muayenesi", Parent: "Z00-Z99"},
		// The one block below chapter level, because the sensitivity of chapter XXI is
		// not the chapter's: a general medical examination is not a sensitive fact and
		// contraceptive management is.
		{Code: "Z30-Z39", Display: "Üreme sağlığı ve gebelik izlemi", Parent: "Z00-Z99", Sensitive: true},
		{Code: "Z30.9", Display: "Kontrasepsiyon yönetimi, tanımlanmamış", Parent: "Z30-Z39", Sensitive: true},
		{Code: "Z31.9", Display: "Üremeye yardımcı yönetim, tanımlanmamış", Parent: "Z30-Z39", Sensitive: true},
		{Code: "Z34.9", Display: "Normal gebelik izlemi, tanımlanmamış", Parent: "Z30-Z39", Sensitive: true},
		{Code: "Z38.0", Display: "Tekil bebek, hastanede doğmuş", Parent: "Z30-Z39", Sensitive: true},

		{Code: "U07.1", Display: "COVID-19, virüs tanımlanmış", Parent: "U00-U99"},
	}
}

// icd10Rows is the whole seeded system: chapters first, so a reader of the table meets the
// hierarchy before its leaves.
func icd10Rows() []icd10Row {
	return append(icd10Chapters(), icd10Codes()...)
}

// ensureICD10 registers the code system and imports the seeded values. Both halves are
// idempotent: a system that is already registered is looked up, and the value import is
// an upsert.
func (s *seeder) ensureICD10(ctx context.Context, tenantID uuid.UUID) error {
	rc := identity.RequestContext{TenantID: tenantID}
	systemID, err := s.ensureCodeSystem(ctx, rc)
	if err != nil {
		return err
	}
	rows := icd10Rows()
	items := make([]catalogdomain.CodeValueInput, 0, len(rows))
	for _, row := range rows {
		attributes := []byte(`{"sensitive":false}`)
		if row.Sensitive {
			attributes = []byte(`{"sensitive":true}`)
		}
		items = append(items, catalogdomain.CodeValueInput{
			Code: row.Code, Display: row.Display, ParentCode: row.Parent,
			ValidFrom: icd10ValidFrom, Active: true, Attributes: attributes,
		})
	}
	summary, err := s.catalog.ImportCodeValues(ctx, rc, systemID, items)
	if err != nil {
		return fmt.Errorf("import ICD-10 values: %w", err)
	}
	fmt.Printf("catalog %-22s %d created, %d updated (%d rows)\n",
		icd10Code, summary.Created, summary.Updated, len(items))
	return nil
}

// ensureCodeSystem returns the tenant's ICD-10 system, registering it the first time.
func (s *seeder) ensureCodeSystem(ctx context.Context, rc identity.RequestContext) (uuid.UUID, error) {
	record, err := s.catalog.CreateCodeSystem(ctx, rc, catalogdomain.NewCodeSystem{
		Code: icd10Code, Name: icd10Name, Version: icd10Version,
		Authority: "WHO",
		// The seeded subset is ours to hand around; the licensed full list is not, and the
		// day it is imported the tenant flips this flag rather than this line changing.
		Licensed:  false,
		ValidFrom: icd10ValidFrom,
	})
	if err == nil {
		return record.ID, nil
	}
	if !errors.Is(err, catalogapp.ErrCodeSystemTaken) {
		return uuid.Nil, fmt.Errorf("register ICD-10: %w", err)
	}
	page, err := s.catalog.ListCodeSystems(ctx, rc, catalogapp.ListFilter{Query: icd10Code, Limit: 50})
	if err != nil {
		return uuid.Nil, fmt.Errorf("look up ICD-10: %w", err)
	}
	for _, item := range page.Items {
		if item.Code == icd10Code && item.Version == icd10Version {
			return item.ID, nil
		}
	}
	return uuid.Nil, errors.New("seed: ICD-10 is registered but could not be found again")
}
