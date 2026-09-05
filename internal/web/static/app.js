// Small progressive enhancements. Every page works without any of this: the
// filter box submits, the sort links navigate, and the forms post.
(function () {
	'use strict';

	// --- Theme toggle -------------------------------------------------------
	var root = document.documentElement;
	var stored = null;
	try { stored = localStorage.getItem('rb-theme'); } catch (e) { /* private mode */ }
	if (stored === 'light' || stored === 'dark') {
		root.setAttribute('data-theme', stored);
	}
	var toggle = document.querySelector('[data-theme-toggle]');
	if (toggle) {
		toggle.addEventListener('click', function () {
			var dark = root.getAttribute('data-theme') === 'dark' ||
				(root.getAttribute('data-theme') !== 'light' &&
					window.matchMedia('(prefers-color-scheme: dark)').matches);
			var next = dark ? 'light' : 'dark';
			root.setAttribute('data-theme', next);
			try { localStorage.setItem('rb-theme', next); } catch (e) { /* ignore */ }
		});
	}

	// --- Ask before anything irreversible -----------------------------------
	document.querySelectorAll('form[data-confirm]').forEach(function (form) {
		form.addEventListener('submit', function (event) {
			if (!window.confirm(form.getAttribute('data-confirm'))) {
				event.preventDefault();
			}
		});
	});

	// --- Filter the table as you type ---------------------------------------
	// The same box submits to the server without JavaScript and gets the same
	// answer. This only saves the round trip, which for a register of a few
	// hundred people is the difference between searching and browsing.
	document.querySelectorAll('input[data-table-filter]').forEach(function (box) {
		var table = document.querySelector(box.getAttribute('data-table-filter'));
		if (!table) { return; }
		var body = table.tBodies[0];
		if (!body) { return; }

		var rows = Array.prototype.slice.call(body.rows).map(function (row) {
			return { row: row, hay: fold(row.textContent) };
		});
		var count = document.querySelector('[data-filter-count]');
		var empty = document.querySelector('[data-filter-empty]');

		var apply = function () {
			var words = fold(box.value).split(/\s+/).filter(Boolean);
			var shown = 0;
			rows.forEach(function (entry) {
				// Every word has to match something, so "anna 14" finds Anna
				// in apartment 1403 rather than every Anna in the house.
				var hit = words.every(function (word) { return entry.hay.indexOf(word) !== -1; });
				entry.row.hidden = !hit;
				if (hit) { shown++; }
			});
			if (empty) { empty.hidden = shown !== 0 || rows.length === 0; }
			if (count) {
				var filtering = words.length > 0;
				count.hidden = !filtering;
				if (filtering) {
					count.textContent = shown + ' / ' + rows.length;
				}
			}
		};

		box.addEventListener('input', apply);
		// Enter would submit and reload with the same result, only slower.
		box.form.addEventListener('submit', function (event) {
			if (document.activeElement === box && box.value.trim() !== '') {
				event.preventDefault();
				apply();
			}
		});
		if (box.value.trim()) { apply(); }
	});

	// fold makes a search insensitive to case and to the Swedish vowels, so
	// that "ostberg" finds Östberg — which is how the name gets typed when
	// somebody is in a hurry.
	function fold(text) {
		return (text || '').toLowerCase()
			.replace(/[åä]/g, 'a').replace(/ö/g, 'o').replace(/é/g, 'e');
	}

	// --- Follow the membership the form is set to ---------------------------
	// The apartment only means anything for somebody who lives here, and
	// "also in" offers the groups a member is not already in by virtue of
	// their kind — offering their own would be a second way to say the same
	// thing. Without JavaScript both stay visible, and the server ignores the
	// combinations that make no sense.
	var kind = document.querySelector('[data-kind-select]');
	if (kind) {
		var only = document.querySelectorAll('[data-only-kind]');
		var notKind = document.querySelectorAll('[data-not-kind]');
		var sync = function () {
			only.forEach(function (field) {
				field.hidden = field.getAttribute('data-only-kind') !== kind.value;
			});
			notKind.forEach(function (field) {
				var same = field.getAttribute('data-not-kind') === kind.value;
				field.hidden = same;
				// A hidden tick must not be submitted: changing somebody's
				// kind would otherwise leave them "also in" the group they
				// have just moved out of.
				if (same) {
					field.querySelectorAll('input[type="checkbox"]').forEach(function (box) {
						box.checked = false;
					});
				}
			});
		};
		kind.addEventListener('change', sync);
		sync();
	}

	// --- The candidate board -------------------------------------------------
	// Everything here is a shortcut for something the page can already do
	// without it: the stage selector on each card moves a candidate, the add
	// button is a link to a real page, and the fields have a form of their
	// own at /kandidater. This makes those three quicker, and steps out of
	// the way the moment anything fails.
	(function board() {
		var root = document.querySelector('[data-board]');
		if (!root || !window.fetch) { return; }

		// Ask the server to change one field, and say whether it took.
		var save = function (id, field, value) {
			var body = new URLSearchParams();
			body.set('falt', field);
			body.set('varde', value);
			body.set('tyst', '1');
			return fetch('/kandidater/' + encodeURIComponent(id) + '/falt', {
				method: 'POST',
				headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
				body: body.toString()
			}).then(function (r) {
				if (!r.ok) { throw new Error('status ' + r.status); }
			});
		};

		var counts = function () {
			root.querySelectorAll('[data-lane]').forEach(function (lane) {
				var n = lane.querySelectorAll('[data-card]').length;
				var tag = lane.querySelector('[data-lane-count]');
				if (tag) { tag.textContent = n; }
			});
		};

		// --- Click a field to change it ---------------------------------
		// One field at a time. Opening eleven inputs to correct a telephone
		// number is what this replaces.
		var editing = null;
		root.addEventListener('click', function (event) {
			var cell = event.target.closest('[data-edit]');
			if (!cell || cell === editing || cell.querySelector('input')) { return; }
			if (editing) { return; }

			var field = cell.getAttribute('data-edit');
			var id = cell.getAttribute('data-id');
			var shown = cell.textContent.trim();
			var raw = cell.getAttribute('data-raw');
			var before = raw !== null ? raw : (shown === '—' ? '' : shown);

			var input = document.createElement('input');
			input.type = cell.getAttribute('data-type') || 'text';
			input.value = before;
			input.className = 'inline-input';
			input.setAttribute('aria-label', cell.getAttribute('data-label') || field);

			editing = cell;
			cell.textContent = '';
			cell.appendChild(input);
			input.focus();
			input.select();

			var done = false;
			var finish = function (keep) {
				if (done) { return; }
				done = true;
				editing = null;
				var value = keep ? input.value.trim() : before;
				var show = function (text) { cell.textContent = text === '' ? '—' : text; };

				if (!keep || value === before) { show(before); return; }

				cell.classList.add('is-saving');
				save(id, field, value).then(function () {
					// The card is dragged and read, not re-rendered, so the
					// displayed form of a date is worked out here rather than
					// asking the server for the whole page again.
					if (cell.hasAttribute('data-raw')) { cell.setAttribute('data-raw', value); }
					show(value);
					cell.classList.remove('is-saving');
					cell.classList.add('is-saved');
					setTimeout(function () { cell.classList.remove('is-saved'); }, 1200);
				}).catch(function () {
					show(before);
					cell.classList.remove('is-saving');
					cell.classList.add('is-failed');
					// A change that did not save must not look as though it
					// did; reload so the card shows what is actually stored.
					setTimeout(function () { window.location.reload(); }, 900);
				});
			};

			input.addEventListener('blur', function () { finish(true); });
			input.addEventListener('keydown', function (e) {
				if (e.key === 'Enter') { e.preventDefault(); finish(true); }
				if (e.key === 'Escape') { e.preventDefault(); finish(false); }
			});
		});

		// A card must not start dragging because somebody selected text in a
		// field they are editing.
		root.addEventListener('mousedown', function (event) {
			var card = event.target.closest('[data-card]');
			if (!card) { return; }
			card.draggable = !event.target.closest('[data-edit], input, select, button, a');
		});

		// --- Drag between lanes -------------------------------------------
		var dragging = null;
		root.addEventListener('dragstart', function (event) {
			var card = event.target.closest('[data-card]');
			if (!card) { return; }
			dragging = card;
			card.classList.add('is-dragging');
			event.dataTransfer.effectAllowed = 'move';
			// Firefox will not start a drag without something on the payload.
            event.dataTransfer.setData('text/plain', card.getAttribute('data-card'));
		});
		root.addEventListener('dragend', function () {
			if (dragging) { dragging.classList.remove('is-dragging'); }
			dragging = null;
			root.querySelectorAll('.is-over').forEach(function (l) { l.classList.remove('is-over'); });
		});
		root.addEventListener('dragover', function (event) {
			if (!dragging) { return; }
			var list = event.target.closest('[data-drop]');
			if (!list) { return; }
			event.preventDefault();
			event.dataTransfer.dropEffect = 'move';
			root.querySelectorAll('.is-over').forEach(function (l) { l.classList.remove('is-over'); });
			list.closest('[data-lane]').classList.add('is-over');
		});
		root.addEventListener('drop', function (event) {
			var list = event.target.closest('[data-drop]');
			if (!list || !dragging) { return; }
			event.preventDefault();

			var card = dragging;
			var lane = list.closest('[data-lane]');
			var stage = lane.getAttribute('data-lane');
			var from = card.parentElement;
			if (from === list) { return; }

			list.appendChild(card);
			counts();

			// Keep the card's own selector honest, so the no-script path and
			// the dragged position never disagree.
			var select = card.querySelector('[data-move]');
			if (select) { select.value = stage; }

			card.classList.add('is-saving');
			save(card.getAttribute('data-card'), 'steg', stage).then(function () {
				card.classList.remove('is-saving');
			}).catch(function () {
				// Put it back where it came from rather than leave the board
				// showing a move that did not happen.
				from.appendChild(card);
				if (select) { select.value = from.closest('[data-lane]').getAttribute('data-lane'); }
				card.classList.remove('is-saving');
				counts();
				window.location.reload();
			});
		});

		// --- The stage selector, without a submit button --------------------
		root.querySelectorAll('[data-move]').forEach(function (select) {
			select.addEventListener('change', function () {
				var card = select.closest('[data-card]');
				var target = root.querySelector('[data-lane="' + select.value + '"] [data-drop]');
				save(card.getAttribute('data-card'), 'steg', select.value).then(function () {
					if (target) { target.appendChild(card); counts(); }
				}).catch(function () { select.form.submit(); });
			});
		});
	})();

	// --- Open a form over the page instead of navigating to it --------------
	document.querySelectorAll('[data-open-dialog]').forEach(function (link) {
		var dialog = document.querySelector(link.getAttribute('data-open-dialog'));
		if (!dialog || typeof dialog.showModal !== 'function') { return; }
		link.addEventListener('click', function (event) {
			event.preventDefault();
			dialog.showModal();
			var first = dialog.querySelector('input, select, textarea');
			if (first) { first.focus(); }
		});
		// Clicking the backdrop closes it, which is what everybody expects.
		dialog.addEventListener('click', function (event) {
			if (event.target === dialog) { dialog.close(); }
		});
	});

	// --- Open the editor when the page was sent back with an error ----------
	if (window.location.hash === '#andra') {
		var editor = document.getElementById('andra');
		if (editor && editor.scrollIntoView) {
			editor.scrollIntoView({ block: 'start' });
		}
	}
})();
