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

	// --- Hide the apartment for a member who does not live here -------------
	var kind = document.querySelector('[data-kind-select]');
	if (kind) {
		var only = document.querySelectorAll('[data-only-kind]');
		var sync = function () {
			only.forEach(function (field) {
				field.hidden = field.getAttribute('data-only-kind') !== kind.value;
			});
		};
		kind.addEventListener('change', sync);
		sync();
	}

	// --- Open the editor when the page was sent back with an error ----------
	if (window.location.hash === '#andra') {
		var editor = document.getElementById('andra');
		if (editor && editor.scrollIntoView) {
			editor.scrollIntoView({ block: 'start' });
		}
	}
})();
