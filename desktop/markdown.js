'use strict';

(function exposeMarkdown(root, factory) {
  const api = factory();
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.MarkdownLite = api;
}(typeof window !== 'undefined' ? window : globalThis, () => {
  function escapeHtml(value) {
    return String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function inline(value) {
    const codes = [];
    let text = String(value).replace(/`([^`\n]+)`/g, (_match, code) => {
      codes.push(`<code>${escapeHtml(code)}</code>`);
      return `\u0000${codes.length - 1}\u0000`;
    });
    text = escapeHtml(text);
    text = text.replace(/\[([^\]]+)\]\(([^\s)]+)\)/g, (_match, label, url) => /^https?:\/\//i.test(url)
      ? `<a href="${url}" target="_blank" rel="noopener noreferrer">${label}</a>`
      : label);
    text = text.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>').replace(/__([^_]+)__/g, '<strong>$1</strong>');
    text = text.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
    return text.replace(/\u0000(\d+)\u0000/g, (_match, index) => codes[Number(index)]);
  }

  function render(markdown) {
    const lines = String(markdown || '').replace(/\r\n?/g, '\n').split('\n');
    const output = [];
    let paragraph = [];
    let list = '';
    const flushParagraph = () => {
      if (paragraph.length) output.push(`<p>${paragraph.map(inline).join('<br>')}</p>`);
      paragraph = [];
    };
    const closeList = () => { if (list) output.push(`</${list}>`); list = ''; };
    for (let index = 0; index < lines.length; index += 1) {
      const line = lines[index];
      if (line.trim().startsWith('```')) {
        flushParagraph(); closeList();
        const language = line.trim().slice(3).trim().replace(/[^a-zA-Z0-9_+-]/g, '');
        const code = [];
        while (++index < lines.length && !lines[index].trim().startsWith('```')) code.push(lines[index]);
        output.push(`<pre><code${language ? ` class="language-${language}"` : ''}>${escapeHtml(code.join('\n'))}</code></pre>`);
        continue;
      }
      const heading = line.match(/^(#{1,4})\s+(.+)$/);
      const unordered = line.match(/^\s*[-*+]\s+(.+)$/);
      const ordered = line.match(/^\s*\d+[.)]\s+(.+)$/);
      const quote = line.match(/^>\s?(.*)$/);
      if (heading) { flushParagraph(); closeList(); output.push(`<h${heading[1].length}>${inline(heading[2])}</h${heading[1].length}>`); }
      else if (unordered || ordered) {
        flushParagraph();
        const nextList = unordered ? 'ul' : 'ol';
        if (list !== nextList) { closeList(); output.push(`<${nextList}>`); list = nextList; }
        output.push(`<li>${inline((unordered || ordered)[1])}</li>`);
      } else if (quote) { flushParagraph(); closeList(); output.push(`<blockquote>${inline(quote[1])}</blockquote>`); }
      else if (!line.trim()) { flushParagraph(); closeList(); }
      else { closeList(); paragraph.push(line); }
    }
    flushParagraph(); closeList();
    return output.join('');
  }

  return { render, escapeHtml };
}));
