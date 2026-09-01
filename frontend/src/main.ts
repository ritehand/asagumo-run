import "./style.css";

// Discord invite link — applied to every [data-discord-invite] anchor
const DISCORD_INVITE_URL = "https://discord.gg/cK9Dw7wavR";

document
  .querySelectorAll<HTMLAnchorElement>("[data-discord-invite]")
  .forEach((a) => {
    a.href = DISCORD_INVITE_URL;
    a.target = "_blank";
    a.rel = "noopener";
  });

// Scroll reveal animation via IntersectionObserver
const revealTargets = document.querySelectorAll<HTMLElement>("[data-reveal]");

const observer = new IntersectionObserver(
  (entries) => {
    for (const entry of entries) {
      if (entry.isIntersecting) {
        entry.target.classList.add("is-visible");
        observer.unobserve(entry.target);
      }
    }
  },
  { threshold: 0.15 }
);

revealTargets.forEach((el) => observer.observe(el));

// Navbar gets a solid background once the hero is scrolled past
const navbar = document.getElementById("navbar");
const hero = document.getElementById("home");

if (navbar && hero) {
  const navObserver = new IntersectionObserver(
    ([entry]) => navbar.classList.toggle("is-scrolled", !entry.isIntersecting),
    { rootMargin: "-80px 0px 0px 0px" }
  );
  navObserver.observe(hero);
}
