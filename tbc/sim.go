package tbc

import (
	"fmt"
	"math"
	"math/rand"
)

var IsDebug = false

func debugFunc(sim *Simulation) func(string, ...interface{}) {
	return func(s string, vals ...interface{}) {
		fmt.Printf("[%0.1f] "+s, append([]interface{}{(float64(sim.currentTick) / float64(TicksPerSecond))}, vals...)...)
	}
}

type Simulation struct {
	CurrentMana float64

	Stats       Stats
	Buffs       Stats     // temp increases
	Equip       Equipment // Current Gear
	activeEquip Equipment // cache of gear that can activate.

	Options       Options
	SpellRotation []int32
	RotationIdx   int

	// ticks until cast is complete
	CastingSpell *Cast

	// timeToRegen := 0
	CDs   map[int32]int
	Auras []Aura // this is array instaed of map to speed up browser perf.

	// Clears and regenerates on each Run call.
	metrics SimMetrics

	rando       *rand.Rand
	rseed       int64
	currentTick int

	debug func(string, ...interface{})
}

type SimMetrics struct {
	TotalDamage float64
	DamageAtOOM float64
	OOMAt       int
	Casts       []*Cast
	ManaAtEnd   int
	Rotation    []string
}

// New sim contructs a simulator with the given stats / equipment / options.
//   Technically we can calculate stats from equip/options but want the ability to override those stats
//   mostly for stat weight purposes.
func NewSim(stats Stats, equip Equipment, options Options) *Simulation {
	if len(options.SpellOrder) == 0 {
		fmt.Printf("[ERROR] No rotation given to sim.\n")
		return nil
	}
	rotIdx := 0
	if options.SpellOrder[0] == "pri" {
		rotIdx = -1
		options.SpellOrder = options.SpellOrder[1:]
	}
	rot := make([]int32, len(options.SpellOrder))

	for i, v := range options.SpellOrder {
		for _, sp := range spells {
			if sp.Name == v {
				rot[i] = sp.ID
				break
			}
		}
	}
	sim := &Simulation{
		RotationIdx:   rotIdx,
		Stats:         stats,
		SpellRotation: rot,
		Options:       options,
		CDs:           map[int32]int{},
		Buffs:         Stats{StatLen: 0},
		Auras:         []Aura{},
		Equip:         equip,
		rseed:         options.RSeed,
		rando:         rand.New(rand.NewSource(options.RSeed)),
		debug:         func(a string, v ...interface{}) {},
	}
	if IsDebug {
		sim.debug = debugFunc(sim)
	}
	return sim
}

func (sim *Simulation) reset() {
	sim.rseed++
	sim.rando.Seed(sim.rseed)

	sim.currentTick = 0
	sim.CurrentMana = sim.Stats[StatMana]
	sim.CastingSpell = nil
	sim.Buffs = Stats{StatLen: 0}
	sim.CDs = map[int32]int{}
	sim.Auras = []Aura{}
	sim.metrics = SimMetrics{}

	// Activate all talents
	if sim.Options.Talents.LightninOverload > 0 {
		sim.addAura(AuraLightningOverload(sim.Options.Talents.LightninOverload))
	}

	// Judgement of Wisdom
	if sim.Options.Buffs.JudgementOfWisdom {
		sim.addAura(AuraJudgementOfWisdom())
	}

	// Activate all permanent item effects.
	for _, item := range sim.Equip {
		if item.Activate != nil && item.ActivateCD == -1 {
			sim.addAura(item.Activate(sim))
		}
	}

	sim.debug("\nSIM RESET\nRotation: %v\n", sim.SpellRotation)
	sim.debug("Effective MP5: %0.1f\n", sim.Stats[StatMP5]+sim.Buffs[StatMP5])
	sim.debug("----------------------\n")
}

func (sim *Simulation) Run(seconds int) SimMetrics {
	// For now use the new 'event' driven state advancement.
	return sim.Run2(seconds)
}

func (sim *Simulation) cleanAuraName(id int32) {
	for i := range sim.Auras {
		if sim.Auras[i].ID == id {
			sim.cleanAura(i)
			break
		}
	}
}
func (sim *Simulation) cleanAura(i int) {
	if sim.Auras[i].OnExpire != nil {
		sim.Auras[i].OnExpire(sim, nil)
	}
	// clean up mem
	sim.Auras[i].OnCast = nil
	sim.Auras[i].OnStruck = nil
	sim.Auras[i].OnSpellHit = nil
	sim.Auras[i].OnExpire = nil

	sim.debug(" -removed: %s- \n", sim.Auras[i].ID)
	sim.Auras = sim.Auras[:i+copy(sim.Auras[i:], sim.Auras[i+1:])]
}

func (sim *Simulation) addAura(a Aura) {
	for i := range sim.Auras {
		if sim.Auras[i].ID == a.ID {
			// TODO: some auras can stack X values. Figure out plan
			sim.Auras[i] = a // replace
			return
		}
	}
	sim.Auras = append(sim.Auras, a)
}

func (sim *Simulation) ChooseSpell() int {
	if sim.RotationIdx == -1 {
		lowestWait := math.MaxInt32
		wasMana := false
		for i := 0; i < len(sim.SpellRotation); i++ {
			so := sim.SpellRotation[i]
			sp := spellmap[so]
			cast := NewCast(sim, sp, sim.Stats[StatSpellDmg], sim.Stats[StatSpellHit], sim.Stats[StatSpellCrit])
			if sim.CDs[so] > 0 { // if
				if sim.CDs[so] < lowestWait {
					lowestWait = sim.CDs[so]
				}
				continue
			}
			if sim.CurrentMana >= cast.ManaCost {
				sim.CastingSpell = cast
				return cast.TicksUntilCast
			}
			manaRegenTicks := int(math.Ceil((cast.ManaCost - sim.CurrentMana) / sim.manaRegen()))
			if manaRegenTicks < lowestWait {
				lowestWait = manaRegenTicks
				wasMana = true
			}
		}
		if wasMana && sim.metrics.OOMAt == 0 { // loop only completes if no spell was found.
			sim.metrics.OOMAt = sim.currentTick / TicksPerSecond
			sim.metrics.DamageAtOOM = sim.metrics.TotalDamage
		}
		return lowestWait
	}

	so := sim.SpellRotation[sim.RotationIdx]
	sp := spellmap[so]
	cast := NewCast(sim, sp, sim.Stats[StatSpellDmg], sim.Stats[StatSpellHit], sim.Stats[StatSpellCrit])
	if sim.CDs[so] < 1 {
		if sim.CurrentMana >= cast.ManaCost {
			sim.CastingSpell = cast
			sim.RotationIdx++
			if sim.RotationIdx == len(sim.SpellRotation) {
				sim.RotationIdx = 0
			}
			return cast.TicksUntilCast
		} else {
			sim.debug("Current Mana %0.0f, Cast Cost: %0.0f\n", sim.CurrentMana, cast.ManaCost)
			if sim.metrics.OOMAt == 0 {
				sim.metrics.OOMAt = sim.currentTick / TicksPerSecond
				sim.metrics.DamageAtOOM = sim.metrics.TotalDamage
			}
			return int(math.Ceil((cast.ManaCost - sim.CurrentMana) / sim.manaRegen()))
		}
	}
	return sim.CDs[so]
}

func (sim *Simulation) Cast(cast *Cast) {
	for _, aur := range sim.Auras {
		if aur.OnCastComplete != nil {
			aur.OnCastComplete(sim, cast)
		}
	}
	sim.debug("Completed Cast (%s)\n", cast.Spell.ID)
	dbgCast := cast.Spell.Name
	if sim.rando.Float64() < cast.Hit {
		dmg := (float64(sim.rando.Intn(int(cast.Spell.MaxDmg-cast.Spell.MinDmg))) + cast.Spell.MinDmg) + (sim.Stats[StatSpellDmg] * cast.Spell.Coeff)
		if cast.DidDmg != 0 { // use the pre-set dmg
			dmg = cast.DidDmg
		}
		cast.DidHit = true
		if sim.rando.Float64() < cast.Crit {
			cast.DidCrit = true
			dmg *= 2
			sim.addAura(AuraElementalFocus(sim.currentTick))
			dbgCast += " crit"
		} else {
			dbgCast += " hit"
		}

		if sim.Options.Talents.Concussion > 0 && cast.Spell.ID == MagicIDLB12 || cast.Spell.ID == MagicIDCL6 {
			// Talent Concussion
			dmg *= 1 + (0.01 * float64(sim.Options.Talents.Concussion))
		}

		// Average Resistance = (Target's Resistance / (Caster's Level * 5)) * 0.75 "AR"
		// P(x) = 50% - 250%*|x - AR| <- where X is chance of resist
		// For now hardcode the 25% chance resist at 2.5% (this assumes bosses have 0 nature resist)
		if sim.rando.Float64() < 0.025 { // chance of 25% resist
			dmg *= .75
			dbgCast += " (partial resist)"
		}
		cast.DidDmg = dmg
		// Apply any effects specific to this cast.
		for _, eff := range cast.Effects {
			eff(sim, cast)
		}
		// Apply any on spell hit effects.
		for _, aur := range sim.Auras {
			if aur.OnSpellHit != nil {
				aur.OnSpellHit(sim, cast)
			}
		}
		sim.metrics.TotalDamage += cast.DidDmg
		sim.metrics.Casts = append(sim.metrics.Casts, cast)
	} else {
		dbgCast += " miss"
	}

	sim.debug("%s: %0.0f\n", dbgCast, cast.DidDmg)
	sim.CurrentMana -= cast.ManaCost
	sim.CastingSpell = nil
	if cast.Spell.Cooldown > 0 {
		sim.CDs[cast.Spell.ID] = cast.Spell.Cooldown * TicksPerSecond
	}
}
