package tbc

func (sim *Simulation) Run2(seconds int) SimMetrics {
	sim.reset()

	ticks := seconds * TicksPerSecond

	// Pop BL at start.
	for i := 0; i < ticks; {
		if sim.CurrentMana < 0 {
			panic("you should never have negative mana.")
		}

		sim.currentTick = i
		advance := sim.Spellcasting(i)

		if sim.Options.ExitOnOOM && sim.metrics.OOMAt > 0 {
			return sim.metrics
		}

		sim.Advance(i, advance)
		i += advance

	}
	sim.metrics.ManaAtEnd = int(sim.CurrentMana)
	return sim.metrics
}

// Spellcasting will cast spells and calculate a new spell to cast.
//  Activates trinkets before spellcasting of off CD.
//  It will pop mana potions if needed.
func (sim *Simulation) Spellcasting(tickID int) int {
	secondID := tickID / TicksPerSecond

	// technically we dont really need this check with the new advancer.
	if sim.CastingSpell != nil && sim.CastingSpell.TicksUntilCast == 0 {
		sim.Cast(sim.CastingSpell)
	}

	if sim.CastingSpell == nil {
		// Activate any specials
		if sim.Options.NumBloodlust > 0 && sim.CDs["bl"] < 1 {
			sim.addAura(ActivateBloodlust(sim))
			sim.Options.NumBloodlust-- // TODO: will this break anything?
		}

		if sim.Options.Talents.ElementalMastery && sim.CDs["elemastery"] < 1 {
			// Apply auras
			sim.addAura(AuraEleMastery())
		}

		// Pop potion before next cast if we have less than the mana provided by the potion minues 1mp5 tick.
		if sim.Stats[StatMana]-sim.CurrentMana+sim.Stats[StatMP5] >= 1500 && sim.CDs["darkrune"] < 1 {
			// Restores 900 to 1500 mana. (2 Min Cooldown)
			sim.CurrentMana += float64(900 + sim.rando.Intn(1500-900))
			sim.CDs["darkrune"] = 120 * TicksPerSecond
			debug("[%d] Used Mana Potion\n", secondID)
		}
		if sim.Stats[StatMana]-sim.CurrentMana+sim.Stats[StatMP5] >= 3000 && sim.CDs["potion"] < 1 {
			// Restores 1800 to 3000 mana. (2 Min Cooldown)
			sim.CurrentMana += float64(1800 + sim.rando.Intn(3000-1800))
			sim.CDs["potion"] = 120 * TicksPerSecond
			debug("[%d] Used Mana Potion\n", secondID)
		}

		// Pop any on-use trinkets
		for _, item := range sim.Equip {
			if item.Activate == nil || item.ActivateCD == -1 { // ignore non-activatable, and always active items.
				continue
			}
			if sim.CDs[item.CoolID] > 0 {
				continue
			}
			if item.Slot == EquipTrinket && sim.CDs["trinket"] > 0 {
				continue
			}
			sim.addAura(item.Activate(sim))
			sim.CDs[item.CoolID] = item.ActivateCD * TicksPerSecond
			if item.Slot == EquipTrinket {
				sim.CDs["trinket"] = 30 * TicksPerSecond
			}
		}

		// Choose next spell
		ticks := sim.ChooseSpell()
		if sim.CastingSpell != nil {
			debug("[%d] Casting %s (%0.1f) ...", secondID, sim.CastingSpell.Spell.ID, float64(sim.CastingSpell.TicksUntilCast)/float64(TicksPerSecond))
		}
		return ticks
	}

	return 1
}

// Advance moves time forward counting down auras, CDs, mana regen, etc
func (sim *Simulation) Advance(tickID int, ticks int) {

	if sim.CastingSpell != nil {
		sim.CastingSpell.TicksUntilCast -= ticks
	}

	// MP5 regen
	sim.CurrentMana += sim.manaRegen() * float64(ticks)

	if sim.CurrentMana > sim.Stats[StatMana] {
		sim.CurrentMana = sim.Stats[StatMana]
	}

	// CDS
	for k := range sim.CDs {
		sim.CDs[k] -= ticks
		if sim.CDs[k] < 1 {
			delete(sim.CDs, k)
		}
	}

	todel := []int{}
	for i := range sim.Auras {
		if sim.Auras[i].Expires <= (tickID + ticks) {
			todel = append(todel, i)
		}
	}
	for i := len(todel) - 1; i >= 0; i-- {
		sim.cleanAura(todel[i])
	}
}

func (sim *Simulation) manaRegen() float64 {
	return ((sim.Stats[StatMP5] + sim.Buffs[StatMP5]) / 5.0) / float64(TicksPerSecond)
}